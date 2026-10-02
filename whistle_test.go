package needle

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

// Metadata fixture only; native tests use the pinned published speech archive.
func testSpeechArchive() []byte {
	blob := testHeadArchive(nil, false)
	const directory, index = 196 + 28*4, 13 + 2*27
	record := blob[directory+index*44:]
	record[0], record[1] = 2, 1
	binary.LittleEndian.PutUint32(record[4:], 13)
	binary.LittleEndian.PutUint64(record[20:], uint64(len(blob)))
	binary.LittleEndian.PutUint64(record[28:], 52)
	blob = append(blob, make([]byte, 52)...)
	for i, value := range []float32{3246, 1, 8192, 8, 4, 9, 128, 3, 80, SpeechSampleRate, 512, 400, 160} {
		binary.LittleEndian.PutUint32(blob[len(blob)-52+i*4:], math.Float32bits(value))
	}
	// The encoder follows the manifest in the speech archive.
	binary.LittleEndian.PutUint32(blob[4:], index+2)
	return blob
}

func TestWeightsKind(t *testing.T) {
	t.Parallel()
	for _, blob := range [][]byte{testHeadArchive(nil, false), testHeadArchive([]uint16{0x4000}, true)} {
		if kind := weightsKind(blob); kind != modelText {
			t.Fatalf("text archive kind = %d", kind)
		}
	}
	blob := testSpeechArchive()
	if kind := weightsKind(blob); kind != modelSpeech {
		t.Fatalf("speech archive kind = %d", kind)
	}
	for size := range len(blob) {
		if weightsKind(blob[:size]) == modelSpeech {
			t.Fatalf("truncated speech archive of %d bytes accepted", size)
		}
	}
	const directory, index = 196 + 28*4, 13 + 2*27
	for name, mutate := range map[string]func([]byte){
		"huge count":       func(b []byte) { binary.LittleEndian.PutUint32(b[4:], math.MaxUint32) },
		"huge layers":      func(b []byte) { binary.LittleEndian.PutUint32(b[40:], math.MaxUint32) },
		"bad offset":       func(b []byte) { binary.LittleEndian.PutUint64(b[directory+index*44+20:], math.MaxUint64) },
		"bad length":       func(b []byte) { binary.LittleEndian.PutUint64(b[directory+index*44+28:], math.MaxUint64) },
		"directory data":   func(b []byte) { binary.LittleEndian.PutUint64(b[directory+index*44+20:], 196) },
		"unknown version":  func(b []byte) { binary.LittleEndian.PutUint32(b[len(b)-48:], math.Float32bits(2)) },
		"unknown rate":     func(b []byte) { binary.LittleEndian.PutUint32(b[len(b)-16:], math.Float32bits(48000)) },
		"unknown manifest": func(b []byte) { binary.LittleEndian.PutUint32(b[len(b)-52:], math.Float32bits(3247)) },
	} {
		t.Run(name, func(t *testing.T) {
			b := append([]byte(nil), blob...)
			mutate(b)
			if kind := weightsKind(b); kind != 0 {
				t.Fatalf("invalid archive kind = %d", kind)
			}
		})
	}
}

func FuzzWeightsKind(f *testing.F) {
	f.Add(testSpeechArchive())
	f.Add(testHeadArchive([]uint16{0x4000}, true))
	f.Fuzz(func(t *testing.T, blob []byte) { weightsKind(blob) })
}

func newTestWhistle(t *testing.T, r *processRuntime) *Whistle {
	t.Helper()
	path := filepath.Join(t.TempDir(), "speech.cact")
	if err := os.WriteFile(path, testSpeechArchive(), 0o600); err != nil {
		t.Fatal(err)
	}
	if r.api.transcribe == nil {
		r.api.transcribe = func(_ *float32, _ int32, _, _ *byte, _ int32, out []byte, _ int32) int32 {
			copy(out, `{"text":"","language":"","ttft_ms":0,"decode_tps":0}`)
			return 0
		}
	}
	r.api.setAudio = func(*byte, *byte, int32) {}
	r.api.completeAudio = func(_ *byte, _ *float32, _ int32, tokens int32, output []byte, capacity int32) int32 {
		return r.api.complete(nil, tokens, output, capacity)
	}
	return &Whistle{runtime: r, weightsPath: path, buffer: make([]byte, DefaultSpeechBufferSize)}
}

func TestWhistleTranscribeAndIndependentWeights(t *testing.T) {
	t.Parallel()
	fake := &fakeNative{}
	a, r := newTestAgent(t, fake, Config{})
	w := newTestWhistle(t, r)
	r.api.transcribe = func(pcm *float32, count int32, language, keywords *byte, timestamps int32, out []byte, capacity int32) int32 {
		if r.mu.TryLock() {
			r.mu.Unlock()
			t.Fatal("transcribe outside shared mutex")
		}
		if count != 2 || !reflect.DeepEqual(unsafe.Slice(pcm, count), []float32{0.5, -0.25}) || readCString(language) != "de" || readCString(keywords) != "Paris\nAda" || timestamps != 1 || capacity != DefaultSpeechBufferSize {
			t.Fatal("incorrect transcription arguments")
		}
		copy(out, `{"text":"Hallo Paris","language":"de","words":[{"word":"Hallo","start":0.1,"end":0.3,"probability":0.9}],"ttft_ms":12,"decode_tps":30}`)
		return 2 // Token count, not the length of the JSON.
	}
	transcript, err := w.Transcribe(context.Background(), []float32{0.5, -0.25}, AudioOptions{Language: "de", Keywords: []string{"Paris", "Ada"}, WordTimestamps: true})
	if err != nil || transcript.Text != "Hallo Paris" || transcript.Language != "de" || len(transcript.Words) != 1 || transcript.Words[0].Probability != 0.9 || transcript.TTFTMS != 12 || transcript.DecodeTPS != 30 {
		t.Fatalf("Transcribe()=%+v, %v", transcript, err)
	}
	if r.active != a || r.activeWeights != "" || fake.loads != 1 || len(fake.systems) != 1 {
		t.Fatal("speech load altered the text conversation")
	}
	// A second speech model switches independently of the text model.
	second := newTestWhistle(t, r)
	r.mu.Lock()
	for _, speech := range []*Whistle{second, w} {
		if err := speech.bindLocked(); err != nil {
			t.Fatal(err)
		}
	}
	r.mu.Unlock()
	if fake.loads != 3 || r.speechWeights != w.weightsPath || !reflect.DeepEqual(r.speechBlob, testSpeechArchive()) || r.active != a {
		t.Fatal("speech switching failed or changed the text model")
	}
}

func TestWhistleRejectsWrongKindWithoutNativeLoad(t *testing.T) {
	t.Parallel()
	fake := &fakeNative{}
	a, r := newTestAgent(t, fake, Config{})
	w := newTestWhistle(t, r)
	r.mu.Lock()
	if err := w.bindLocked(); err != nil {
		t.Fatal(err)
	}
	r.mu.Unlock()
	speechBlob := r.speechBlob
	wrongSpeech := newTestWhistle(t, r)
	if err := os.WriteFile(wrongSpeech.weightsPath, testHeadArchive([]uint16{0x4000}, true), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := wrongSpeech.Transcribe(context.Background(), nil, AudioOptions{}); err == nil || !strings.Contains(err.Error(), "not a recognized speech model") {
		t.Fatalf("text-as-speech error = %v", err)
	}
	wrongText, err := prepareAgent(Config{WeightsPath: w.weightsPath})
	if err != nil {
		t.Fatal(err)
	}
	wrongText.runtime = r
	if _, err := wrongText.Complete(context.Background(), "hello", 1); err == nil || !strings.Contains(err.Error(), "not a recognized text model") {
		t.Fatalf("speech-as-text error = %v", err)
	}
	if fake.loads != 1 || r.active != a || r.speechWeights != w.weightsPath || !reflect.DeepEqual(r.speechBlob, speechBlob) {
		t.Fatal("wrong-kind rejection mutated loaded models")
	}
}

func TestWhistleCompleteIsolatesAudioOptions(t *testing.T) {
	t.Parallel()
	fake := &fakeNative{}
	a, r := newTestAgent(t, fake, Config{Stateless: true})
	w := newTestWhistle(t, r)
	var events []string
	r.api.setAudio = func(language, keywords *byte, timestamps int32) {
		if r.mu.TryLock() {
			r.mu.Unlock()
			t.Fatal("audio options outside shared mutex")
		}
		events = append(events, fmt.Sprintf("%s/%s/%d", readCString(language), readCString(keywords), timestamps))
	}
	code := int32(3)
	r.api.lastError = func() string { events = append(events, "error"); return "audio detail" }
	r.api.completeAudio = func(input *byte, pcm *float32, samples, tokens int32, out []byte, capacity int32) int32 {
		if input != nil || pcm == nil || samples != 0 || tokens != DefaultMaxNewTokens || capacity != DefaultBufferSize {
			t.Fatal("invalid empty audio completion ABI")
		}
		events = append(events, "complete")
		copy(out, `{"type":"call","function_calls":[],"audio_text":"","audio_language":"","audio_words":[],"audio_ttft_ms":0,"audio_decode_tps":0}`)
		return code
	}
	for _, options := range []AudioOptions{{Language: "fr", Keywords: []string{"Ada"}, WordTimestamps: true}, {}} {
		response, err := w.Complete(context.Background(), a, nil, options, 0)
		if err != nil || response.AudioText == nil || *response.AudioText != "" || response.AudioLanguage == nil || response.AudioTTFTMS == nil || response.AudioDecodeTPS == nil {
			t.Fatalf("empty audio response = %+v, %v", response, err)
		}
	}
	code = -7
	if _, err := w.Complete(context.Background(), a, nil, AudioOptions{Language: "de"}, 0); err == nil || !strings.Contains(err.Error(), "audio detail") {
		t.Fatalf("audio error = %v", err)
	}
	want := []string{"fr/Ada/1", "complete", "//0", "//0", "complete", "//0", "de//0", "complete", "error", "//0"}
	if !reflect.DeepEqual(events, want) || fake.resets != 3 {
		t.Fatalf("events=%v want=%v resets=%d", events, want, fake.resets)
	}
}

func TestWhistleInvalidInputAndErrors(t *testing.T) {
	t.Parallel()
	fake := &fakeNative{}
	a, r := newTestAgent(t, fake, Config{})
	w := newTestWhistle(t, r)
	for _, test := range []struct {
		name    string
		pcm     []float32
		options AudioOptions
	}{
		{"too long", make([]float32, MaxSpeechSamples+1), AudioOptions{}},
		{"NaN", []float32{float32(math.NaN())}, AudioOptions{}},
		{"infinity", []float32{float32(math.Inf(1))}, AudioOptions{}},
		{"outside range", []float32{-1.01}, AudioOptions{}},
		{"language", nil, AudioOptions{Language: "sr"}},
		{"keyword NUL", nil, AudioOptions{Keywords: []string{"Ada\x00"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := w.Transcribe(context.Background(), test.pcm, test.options); err == nil {
				t.Fatal("invalid transcription accepted")
			}
			if _, err := w.Complete(context.Background(), a, test.pcm, test.options, 1); err == nil {
				t.Fatal("invalid audio completion accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.Transcribe(ctx, nil, AudioOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled transcription = %v", err)
	}
	if _, err := w.Complete(ctx, a, nil, AudioOptions{}, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled audio = %v", err)
	}
	if _, err := w.Complete(context.Background(), a, nil, AudioOptions{}, -1); err == nil {
		t.Fatal("negative token count accepted")
	}
	if _, err := w.Complete(context.Background(), nil, nil, AudioOptions{}, 1); err == nil {
		t.Fatal("missing text agent accepted")
	}
	if fake.loads != 0 || fake.resets != 0 {
		t.Fatal("invalid inputs touched native model state")
	}
	for _, output := range []string{"invalid JSON", strings.Repeat("x", DefaultSpeechBufferSize)} {
		r.api.transcribe = func(_ *float32, _ int32, _, _ *byte, _ int32, out []byte, _ int32) int32 { copy(out, output); return 0 }
		if _, err := w.Transcribe(context.Background(), nil, AudioOptions{}); err == nil {
			t.Fatal("invalid transcript accepted")
		}
	}
	r.api.lastError = func() string { return "transcription detail" }
	r.api.transcribe = func(_ *float32, _ int32, _, _ *byte, _ int32, _ []byte, _ int32) int32 { return -2 }
	if _, err := w.Transcribe(context.Background(), nil, AudioOptions{}); err == nil || !strings.Contains(err.Error(), "transcription detail") {
		t.Fatalf("native error = %v", err)
	}
	r.api.transcribe = nil
	if _, err := w.Transcribe(context.Background(), nil, AudioOptions{}); err == nil || !strings.Contains(err.Error(), "does not support speech") {
		t.Fatalf("legacy error = %v", err)
	}
	for _, size := range []int{1, int(math.MaxInt32) + 1} {
		if _, err := NewWhistle(context.Background(), WhistleConfig{BufferSize: size}); err == nil {
			t.Fatal("invalid buffer size accepted")
		}
	}
}

func TestFetchWhistleUsesVerifiedCacheWithoutTextWeights(t *testing.T) {
	t.Parallel()
	cache := t.TempDir()
	platform := PlatformDarwinARM64
	for _, artifact := range []engineArtifact{artifacts[platform], speechWeights} {
		content := []byte("cached artifact " + artifact.libraryName)
		path := filepath.Join(cache, artifact.libraryName)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(content)
		if err := os.WriteFile(path+".sha256", []byte(fmt.Sprintf("%s\n%x\n", artifact.checksum, hash)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	client := &http.Client{Transport: rejectCacheDownload{}}
	path, err := FetchWhistle(context.Background(), FetchOptions{Platform: platform, CacheDir: cache, Client: client})
	if err != nil || path != filepath.Join(cache, artifacts[platform].libraryName) {
		t.Fatalf("offline Whistle path=%q err=%v", path, err)
	}
	if _, err := os.Stat(filepath.Join(cache, baseWeights.filename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected text weights: %v", err)
	}
	if _, err := FetchWhistle(context.Background(), FetchOptions{Generation: 2}); err == nil {
		t.Fatal("Needle 2 speech accepted")
	}
}

func TestNativeWhistle(t *testing.T) {
	if os.Getenv("NEEDLE_TEST_NATIVE") != "1" {
		t.Skip("set NEEDLE_TEST_NATIVE=1 to test native speech")
	}
	ctx := context.Background()
	w, err := NewWhistle(ctx, WhistleConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, pcm := range [][]float32{nil, make([]float32, SpeechSampleRate)} {
		transcript, err := w.Transcribe(ctx, pcm, AudioOptions{WordTimestamps: true})
		if err != nil || transcript.Text != "" || transcript.Language != "" || len(transcript.Words) != 0 {
			t.Fatalf("silence transcript = %+v, %v", transcript, err)
		}
	}
	if weightsKind(w.runtime.speechBlob) != modelSpeech {
		t.Fatal("published speech archive kind not recognized")
	}
	type weatherArguments struct {
		City string `json:"city"`
	}
	a, err := New(ctx, Config{Tools: []Tool{{Schema: SchemaFor[weatherArguments]("get_weather", "Current weather for a city.")}}})
	if err != nil {
		t.Fatal(err)
	}
	if weightsKind(w.runtime.activeBlob) != modelText {
		t.Fatal("published text archive kind not recognized")
	}
	// Speech reload/transcription must preserve a pending text tool call.
	path := filepath.Join(t.TempDir(), "speech.cact")
	if err := os.WriteFile(path, w.runtime.speechBlob, 0o600); err != nil {
		t.Fatal(err)
	}
	var baseline Response
	for _, interrupt := range []bool{false, true} {
		if err := a.Reset(ctx); err != nil {
			t.Fatal(err)
		}
		initial, err := a.Complete(ctx, "what is the weather in Paris?", DefaultMaxNewTokens)
		if err != nil || len(initial.FunctionCalls) != 1 || initial.FunctionCalls[0].Name != "get_weather" {
			t.Fatalf("initial call=%+v err=%v", initial, err)
		}
		if interrupt {
			other, err := NewWhistle(ctx, WhistleConfig{WeightsPath: path})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := other.Transcribe(ctx, nil, AudioOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		final, err := a.Complete(ctx, `[{"city":"Paris","weather":"rain, 14C"}]`, DefaultMaxNewTokens)
		if err != nil || !final.Success || final.Type != ResponseRespond {
			t.Fatalf("continuation=%+v err=%v", final, err)
		}
		if interrupt && (final.Type != baseline.Type || final.Reasoning != baseline.Reasoning || !reflect.DeepEqual(final.FunctionCalls, baseline.FunctionCalls) || !reflect.DeepEqual(final.Confidence, baseline.Confidence)) {
			t.Fatalf("speech changed conversation: got=%+v baseline=%+v", final, baseline)
		}
		baseline = final
	}
	// Both kinds are already loaded: a bitmask alone cannot reject these swaps.
	if _, err := New(ctx, Config{WeightsPath: path}); err == nil || !strings.Contains(err.Error(), "not a recognized text model") {
		t.Fatalf("speech as text: %v", err)
	}
	if _, err := NewWhistle(ctx, WhistleConfig{WeightsPath: w.runtime.activeWeights}); err == nil || !strings.Contains(err.Error(), "not a recognized speech model") {
		t.Fatalf("text as speech: %v", err)
	}
	for _, pcm := range [][]float32{nil, make([]float32, SpeechSampleRate)} {
		if err := a.Reset(ctx); err != nil {
			t.Fatal(err)
		}
		response, err := w.Complete(ctx, a, pcm, AudioOptions{Language: "en", WordTimestamps: true}, DefaultMaxNewTokens)
		if err != nil || !response.Success || response.AudioText == nil || *response.AudioText != "" || response.AudioLanguage == nil || response.AudioTTFTMS == nil || response.AudioDecodeTPS == nil {
			t.Fatalf("silence audio completion=%+v err=%v", response, err)
		}
	}
	// Text switches preserve speech just as speech switches preserve text.
	other, err := New(ctx, Config{System: "device: test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Complete(ctx, "hello", DefaultMaxNewTokens); err != nil {
		t.Fatal(err)
	}
	if transcript, err := w.Transcribe(ctx, nil, AudioOptions{}); err != nil || transcript.Text != "" {
		t.Fatalf("speech after text switch=%+v err=%v", transcript, err)
	}
}
