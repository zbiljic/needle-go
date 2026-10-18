package needle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// StreamTranscript is one step of live transcription. Text and Words are the
// words the step committed, with times in seconds from the start of the
// stream, and Pending is the unconfirmed tail. Received counts the seconds of
// audio so far and PassMS the time the step took. Join the texts of
// consecutive steps with a space.
type StreamTranscript struct {
	Text     string          `json:"text"`
	Words    []WordTimestamp `json:"words"`
	Pending  string          `json:"pending"`
	Language string          `json:"language"`
	Received float64         `json:"received"`
	PassMS   float64         `json:"pass_ms"`
}

// Stream is a live transcription started by Whistle.Stream. Feed about one
// second of PCM per Process call and finish with Stop, which commits the
// tail. The engine holds one stream per process: while a stream is open,
// speech calls other than its own Process and Stop return ErrStreamActive.
// A failed Process ends the stream. Closing the Whistle also ends it.
type Stream struct {
	whistle  *Whistle
	language []byte
	keywords []byte
	buffer   []byte
	stopped  bool
}

// Stream prepares a live transcription with the language and keywords of
// options; WordTimestamps is ignored because every step carries word times.
// Nothing reaches the engine until the first Process.
func (w *Whistle) Stream(options AudioOptions) (*Stream, error) {
	language, keywords, _, err := prepareAudio(nil, options)
	if err != nil {
		return nil, err
	}
	size := len(w.buffer)
	if size == 0 {
		size = DefaultSpeechBufferSize
	}
	return &Stream{whistle: w, language: language, keywords: keywords, buffer: make([]byte, size)}, nil
}

// Process appends PCM to the stream and returns the words it committed.
func (s *Stream) Process(ctx context.Context, pcm []float32) (StreamTranscript, error) {
	if err := ctx.Err(); err != nil {
		return StreamTranscript{}, err
	}
	if err := checkPCM(pcm); err != nil {
		return StreamTranscript{}, err
	}
	r := s.whistle.runtime
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return StreamTranscript{}, err
	}
	api, err := s.bindLocked()
	if err != nil {
		return StreamTranscript{}, err
	}
	s.reserve(len(pcm))
	clear(s.buffer)
	code := api.streamProcess(pcmPointer(pcm), int32(len(pcm)), bytePointer(s.language), bytePointer(s.keywords), s.buffer, int32(len(s.buffer)))
	r.stream = s
	step, err := s.decode(api, "stream", code)
	if err != nil {
		// The engine's stream state is unknown after a failure; end it so
		// other speech calls are not blocked.
		s.endLocked()
		return StreamTranscript{}, err
	}
	return step, nil
}

// Stop ends the stream and returns the words committed from its tail. A
// stopped stream cannot be resumed; start another with Whistle.Stream.
func (s *Stream) Stop(ctx context.Context) (StreamTranscript, error) {
	if err := ctx.Err(); err != nil {
		return StreamTranscript{}, err
	}
	r := s.whistle.runtime
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return StreamTranscript{}, err
	}
	api, err := s.bindLocked()
	if err != nil {
		return StreamTranscript{}, err
	}
	clear(s.buffer)
	code := api.streamStop(s.buffer, int32(len(s.buffer)))
	r.stream = nil
	s.stopped = true
	return s.decode(api, "stream stop", code)
}

// bindLocked checks that this stream may use the engine. The caller must hold
// the runtime mutex.
func (s *Stream) bindLocked() (*nativeAPI, error) {
	if s.stopped {
		return nil, errors.New("needle: stream is stopped")
	}
	r := s.whistle.runtime
	if r.stream != nil && r.stream != s {
		return nil, ErrStreamActive
	}
	if err := s.whistle.bindLocked(); err != nil {
		return nil, err
	}
	if r.api.streamProcess == nil {
		return nil, fmt.Errorf("%w: engine does not support live transcription; use Needle 3.2 or newer", ErrSpeechUnsupported)
	}
	return r.api, nil
}

// endLocked stops the engine's stream without reporting its tail. The caller
// must hold the runtime mutex.
func (s *Stream) endLocked() {
	r := s.whistle.runtime
	if r.stream == s && r.api != nil && r.api.streamStop != nil {
		clear(s.buffer)
		r.api.streamStop(s.buffer, int32(len(s.buffer)))
	}
	if r.stream == s {
		r.stream = nil
	}
	s.stopped = true
}

func (s *Stream) reserve(samples int) {
	if needed := samples / SpeechSampleRate * speechOutputBytesPerSecond; needed > len(s.buffer) {
		s.buffer = make([]byte, needed)
	}
}

func (s *Stream) decode(api *nativeAPI, operation string, code int32) (StreamTranscript, error) {
	data, err := nativeOutput(api, operation, code, s.buffer)
	if err != nil {
		return StreamTranscript{}, err
	}
	var step StreamTranscript
	if err := json.Unmarshal(data, &step); err != nil {
		return StreamTranscript{}, fmt.Errorf("needle: decode stream step: %w", err)
	}
	return step, nil
}
