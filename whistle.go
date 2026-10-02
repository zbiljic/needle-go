package needle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

const (
	// SpeechSampleRate is the required rate for mono float32 PCM.
	SpeechSampleRate = 16000
	// MaxSpeechSamples limits a clip to 30 seconds.
	MaxSpeechSamples = 30 * SpeechSampleRate
	// DefaultSpeechBufferSize accommodates transcripts with word timestamps.
	DefaultSpeechBufferSize = 1 << 18
)

// AudioOptions controls language detection, keyword biasing and word timestamps.
type AudioOptions struct {
	// Language is empty for detection or en, de, fr, es, it, nl or pl.
	Language       string
	Keywords       []string
	WordTimestamps bool
}

// WordTimestamp describes a recognized word, with times in seconds.
type WordTimestamp struct {
	Word        string  `json:"word"`
	Start       float64 `json:"start"`
	End         float64 `json:"end"`
	Probability float64 `json:"probability"`
}

// Transcript is a Whistle response. Silence has empty Text and Language.
type Transcript struct {
	Text      string          `json:"text"`
	Language  string          `json:"language"`
	Words     []WordTimestamp `json:"words,omitempty"`
	TTFTMS    float64         `json:"ttft_ms"`
	DecodeTPS float64         `json:"decode_tps"`
}

// WhistleConfig selects speech weights and a Needle 3.1-compatible engine.
// LibraryPath and CacheDir follow Config. Empty WeightsPath fetches Whistle.
type WhistleConfig struct {
	WeightsPath string
	LibraryPath string
	CacheDir    string
	BufferSize  int
}

// Whistle transcribes caller-provided 16 kHz mono float32 PCM in [-1, 1].
// Speech and Needle 3 text models share a serialized process-global engine.
type Whistle struct {
	runtime     *processRuntime
	weightsPath string
	buffer      []byte
}

// NewWhistle loads speech weights without downloading or initializing text weights.
func NewWhistle(ctx context.Context, config WhistleConfig) (*Whistle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size := config.BufferSize
	if size == 0 {
		size = DefaultSpeechBufferSize
	}
	if size < 2 || size > math.MaxInt32 {
		return nil, fmt.Errorf("needle: invalid buffer size %d", size)
	}
	weightsPath := config.WeightsPath
	if weightsPath == "" {
		var err error
		weightsPath, err = fetchSpeechWeights(ctx, FetchOptions{CacheDir: config.CacheDir})
		if err != nil {
			return nil, err
		}
	}
	weightsPath, err := filepath.Abs(weightsPath)
	if err != nil {
		return nil, fmt.Errorf("needle: resolve speech weights path: %w", err)
	}
	libraryPath, err := resolveLibraryPath(ctx, Config{Generation: 3, LibraryPath: config.LibraryPath, CacheDir: config.CacheDir})
	if err != nil {
		return nil, err
	}
	w := &Whistle{runtime: runtimes[3], weightsPath: weightsPath, buffer: make([]byte, size)}
	w.runtime.mu.Lock()
	defer w.runtime.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := w.runtime.ensureLibraryLocked(libraryPath); err != nil {
		return nil, err
	}
	if err := w.bindLocked(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Whistle) bindLocked() error {
	api := w.runtime.api
	if api == nil || api.transcribe == nil || api.setAudio == nil || api.completeAudio == nil {
		return errors.New("needle: engine does not support speech; use Needle 3.1 or newer")
	}
	if w.runtime.speechWeights == w.weightsPath {
		return nil
	}
	blob, err := os.ReadFile(w.weightsPath)
	if err != nil {
		return fmt.Errorf("needle: read speech weights: %w", err)
	}
	if weightsKind(blob) != modelSpeech {
		return errors.New("needle: weights are not a recognized speech model")
	}
	if code := api.load(blob, uint64(len(blob))); code < 0 {
		w.runtime.speechWeights = ""
		return api.failure("load speech weights", code, "")
	}
	w.runtime.speechBlob, w.runtime.speechWeights = blob, w.weightsPath
	return nil
}

// Transcribe accepts up to 30 seconds of PCM. Empty audio and silence return an
// empty transcript. Calls are serialized with all text and speech native work.
func (w *Whistle) Transcribe(ctx context.Context, pcm []float32, options AudioOptions) (Transcript, error) {
	if err := ctx.Err(); err != nil {
		return Transcript{}, err
	}
	language, keywords, timestamps, err := prepareAudio(pcm, options)
	if err != nil {
		return Transcript{}, err
	}
	w.runtime.mu.Lock()
	defer w.runtime.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Transcript{}, err
	}
	if err := w.bindLocked(); err != nil {
		return Transcript{}, err
	}
	clear(w.buffer)
	code := w.runtime.api.transcribe(pcmPointer(pcm), int32(len(pcm)), bytePointer(language), bytePointer(keywords), timestamps, w.buffer, int32(len(w.buffer)))
	data, err := nativeOutput(w.runtime.api, "transcribe", code, w.buffer)
	if err != nil {
		return Transcript{}, err
	}
	var transcript Transcript
	if err := json.Unmarshal(data, &transcript); err != nil {
		return Transcript{}, fmt.Errorf("needle: decode transcript: %w", err)
	}
	return transcript, nil
}

// Complete transcribes PCM and completes a raw Needle 3 turn inside the engine.
// It returns tool calls without executing handlers or applying ValidateResponse.
// agent must be a Needle 3 Agent created by New and share this Whistle's engine.
func (w *Whistle) Complete(ctx context.Context, textAgent Agent, pcm []float32, options AudioOptions, maxNewTokens int) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	a, ok := textAgent.(*agent)
	if !ok || a == nil || a.generation != 3 || a.runtime != w.runtime {
		return Response{}, errors.New("needle: audio completion requires a Needle 3 agent sharing the speech engine")
	}
	if maxNewTokens == 0 {
		maxNewTokens = DefaultMaxNewTokens
	}
	if maxNewTokens < 1 || maxNewTokens > math.MaxInt32 {
		return Response{}, fmt.Errorf("needle: invalid max new tokens %d", maxNewTokens)
	}
	language, keywords, timestamps, err := prepareAudio(pcm, options)
	if err != nil {
		return Response{}, err
	}
	w.runtime.mu.Lock()
	defer w.runtime.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if err := w.bindLocked(); err != nil {
		return Response{}, err
	}
	if err := w.runtime.bindLocked(a); err != nil {
		return Response{}, err
	}
	api := w.runtime.api
	if a.stateless {
		api.reset()
	}
	api.setAudio(bytePointer(language), bytePointer(keywords), timestamps)
	defer api.setAudio(nil, nil, 0)
	clear(a.buffer)
	code := api.completeAudio(nil, pcmPointer(pcm), int32(len(pcm)), int32(maxNewTokens), a.buffer, int32(len(a.buffer)))
	return a.decodeResponse(code)
}

func prepareAudio(pcm []float32, options AudioOptions) ([]byte, []byte, int32, error) {
	if len(pcm) > MaxSpeechSamples {
		return nil, nil, 0, errors.New("needle: audio exceeds 30 seconds at 16000 Hz")
	}
	for i, sample := range pcm {
		if sample != sample || sample < -1 || sample > 1 {
			return nil, nil, 0, fmt.Errorf("needle: invalid PCM sample at index %d; want finite values in [-1, 1]", i)
		}
	}
	switch options.Language {
	case "", "en", "de", "fr", "es", "it", "nl", "pl":
	default:
		return nil, nil, 0, fmt.Errorf("needle: unsupported speech language %q", options.Language)
	}
	language, err := cString("speech language", options.Language, true)
	if err != nil {
		return nil, nil, 0, err
	}
	keywords, err := cString("speech keywords", strings.Join(options.Keywords, "\n"), true)
	if err != nil {
		return nil, nil, 0, err
	}
	var timestamps int32
	if options.WordTimestamps {
		timestamps = 1
	}
	return language, keywords, timestamps, nil
}

func pcmPointer(pcm []float32) *float32 {
	if len(pcm) == 0 {
		// Audio completion requires nonnull PCM even for an empty clip.
		return new(float32)
	}
	return &pcm[0]
}
