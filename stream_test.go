package needle

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

func fakeStream(t *testing.T, r *processRuntime, events *[]string) {
	t.Helper()
	r.api.streamProcess = func(pcm *float32, count int32, language, keywords *byte, out []byte, capacity int32) int32 {
		if r.mu.TryLock() {
			r.mu.Unlock()
			t.Fatal("stream process outside shared mutex")
		}
		if int(capacity) != len(out) {
			t.Fatalf("capacity %d != buffer %d", capacity, len(out))
		}
		var samples []float32
		if count > 0 {
			samples = unsafe.Slice(pcm, count)
		}
		*events = append(*events, fmt.Sprintf("process/%v/%s/%s/%d", samples, readCString(language), readCString(keywords), capacity))
		copy(out, `{"text":"hello","words":[{"word":"hello","start":0.1,"end":0.4,"probability":0.9}],"pending":"there","language":"en","received":1,"pass_ms":12}`)
		return 1
	}
	r.api.streamStop = func(out []byte, capacity int32) int32 {
		*events = append(*events, fmt.Sprintf("stop/%d", capacity))
		copy(out, `{"text":"there","words":[],"pending":"","language":"en","received":2,"pass_ms":3}`)
		return 1
	}
}

func TestStreamProcessAndStop(t *testing.T) {
	t.Parallel()
	fake := &fakeNative{}
	a, r := newTestAgent(t, fake, Config{})
	w := newTestWhistle(t, r)
	var events []string
	fakeStream(t, r, &events)
	ctx := context.Background()
	s, err := w.Stream(AudioOptions{Language: "en", Keywords: []string{"Paris", "Ada"}, WordTimestamps: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.stream != nil || fake.loads != 0 {
		t.Fatal("Stream() touched the engine")
	}
	step, err := s.Process(ctx, []float32{0.5, -0.25})
	if err != nil || step.Text != "hello" || step.Pending != "there" || step.Language != "en" || step.Received != 1 || step.PassMS != 12 || len(step.Words) != 1 || step.Words[0].End != 0.4 {
		t.Fatalf("Process() = %+v, %v", step, err)
	}
	if r.stream != s || fake.loads != 1 {
		t.Fatal("stream is not bound to the engine")
	}
	// Other speech work waits for Stop; the open stream's own calls proceed.
	if _, err := w.Transcribe(ctx, nil, AudioOptions{}); !errors.Is(err, ErrStreamActive) {
		t.Fatalf("Transcribe() during stream error = %v", err)
	}
	if _, err := w.Complete(ctx, a, nil, AudioOptions{}, 0); !errors.Is(err, ErrStreamActive) {
		t.Fatalf("Complete() during stream error = %v", err)
	}
	second, err := w.Stream(AudioOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Process(ctx, nil); !errors.Is(err, ErrStreamActive) {
		t.Fatalf("second stream Process() error = %v", err)
	}
	if _, err := second.Stop(ctx); !errors.Is(err, ErrStreamActive) {
		t.Fatalf("second stream Stop() error = %v", err)
	}
	other := newTestWhistle(t, r)
	r.mu.Lock()
	err = other.bindLocked()
	r.mu.Unlock()
	if !errors.Is(err, ErrStreamActive) || r.speechWeights != w.weightsPath {
		t.Fatalf("speech weights switched during stream: %v", err)
	}
	if _, err := s.Process(ctx, nil); err != nil {
		t.Fatalf("empty chunk error = %v", err)
	}
	tail, err := s.Stop(ctx)
	if err != nil || tail.Text != "there" || tail.Received != 2 || r.stream != nil {
		t.Fatalf("Stop() = %+v, %v, stream=%p", tail, err, r.stream)
	}
	for _, call := range []func() error{
		func() error { _, err := s.Process(ctx, nil); return err },
		func() error { _, err := s.Stop(ctx); return err },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "stopped") {
			t.Fatalf("stopped stream error = %v", err)
		}
	}
	if _, err := w.Transcribe(ctx, nil, AudioOptions{}); err != nil {
		t.Fatalf("Transcribe() after stream error = %v", err)
	}
	want := []string{
		fmt.Sprintf("process/[0.5 -0.25]/en/Paris\nAda/%d", DefaultSpeechBufferSize),
		fmt.Sprintf("process/[]/en/Paris\nAda/%d", DefaultSpeechBufferSize),
		fmt.Sprintf("stop/%d", DefaultSpeechBufferSize),
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %q, want %q", events, want)
	}
}

func TestStreamFailureEndsStream(t *testing.T) {
	t.Parallel()
	fake := &fakeNative{}
	_, r := newTestAgent(t, fake, Config{})
	w := newTestWhistle(t, r)
	var events []string
	fakeStream(t, r, &events)
	process := r.api.streamProcess
	r.api.streamProcess = func(pcm *float32, count int32, language, keywords *byte, out []byte, capacity int32) int32 {
		process(pcm, count, language, keywords, out, capacity)
		return -2
	}
	r.api.lastError = func() string { return "stream detail" }
	ctx := context.Background()
	s, err := w.Stream(AudioOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Process(ctx, nil)
	var native *NativeError
	if !errors.As(err, &native) || native.Operation != "stream" || native.Code != -2 || native.Detail != "stream detail" {
		t.Fatalf("Process() error = %#v", err)
	}
	if r.stream != nil || len(events) != 2 || !strings.HasPrefix(events[1], "stop/") {
		t.Fatalf("failed stream was not ended: stream=%p events=%q", r.stream, events)
	}
	if _, err := s.Process(ctx, nil); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("Process() after failure error = %v", err)
	}
	if _, err := w.Transcribe(ctx, nil, AudioOptions{}); err != nil {
		t.Fatalf("Transcribe() after failed stream error = %v", err)
	}
}

func TestStreamUnsupportedAndInvalidInput(t *testing.T) {
	t.Parallel()
	fake := &fakeNative{}
	_, r := newTestAgent(t, fake, Config{})
	w := newTestWhistle(t, r)
	ctx := context.Background()
	for _, options := range []AudioOptions{{Language: "sr"}, {Keywords: []string{"Ada\x00"}}} {
		if _, err := w.Stream(options); err == nil {
			t.Fatalf("Stream(%+v) accepted", options)
		}
	}
	s, err := w.Stream(AudioOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Engine 3.1 has speech without streaming.
	if _, err := s.Process(ctx, nil); !errors.Is(err, ErrSpeechUnsupported) {
		t.Fatalf("Process() on 3.1 engine error = %v", err)
	}
	if _, err := s.Stop(ctx); !errors.Is(err, ErrSpeechUnsupported) {
		t.Fatalf("Stop() on 3.1 engine error = %v", err)
	}
	var events []string
	fakeStream(t, r, &events)
	for _, pcm := range [][]float32{{float32(math.NaN())}, {1.5}} {
		if _, err := s.Process(ctx, pcm); err == nil {
			t.Fatal("invalid PCM accepted")
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Process(canceled, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Process() error = %v", err)
	}
	if _, err := s.Stop(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Stop() error = %v", err)
	}
	if len(events) != 0 || r.stream != nil {
		t.Fatalf("invalid input reached the engine: %q", events)
	}
	r.api.streamProcess = func(_ *float32, _ int32, _, _ *byte, out []byte, _ int32) int32 { copy(out, "not json"); return 0 }
	if _, err := s.Process(ctx, nil); err == nil || !strings.Contains(err.Error(), "decode stream step") {
		t.Fatalf("invalid step error = %v", err)
	}
}

func TestStreamGrowsBufferAndCloseStopsStream(t *testing.T) {
	t.Parallel()
	fake := &fakeNative{}
	_, r := newTestAgent(t, fake, Config{})
	w := newTestWhistle(t, r)
	var events []string
	fakeStream(t, r, &events)
	ctx := context.Background()
	s, err := w.Stream(AudioOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Process(ctx, make([]float32, 100*SpeechSampleRate)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if r.stream != nil {
		t.Fatal("Close() left the stream open")
	}
	if _, err := s.Process(ctx, nil); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("Process() after Close() error = %v", err)
	}
	want := []string{
		fmt.Sprintf("process/[]/%s/%s/%d", "", "", 100*speechOutputBytesPerSecond),
		fmt.Sprintf("stop/%d", 100*speechOutputBytesPerSecond),
	}
	// The fake records an empty slice for silence of any length.
	events[0] = strings.Replace(events[0], fmt.Sprintf("process/%v/", make([]float32, 100*SpeechSampleRate)), "process/[]/", 1)
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %q, want %q", events, want)
	}
	other := newTestWhistle(t, r)
	if _, err := other.Transcribe(ctx, nil, AudioOptions{}); err != nil {
		t.Fatalf("Transcribe() after Close() error = %v", err)
	}
}
