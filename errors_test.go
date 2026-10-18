package needle

import (
	"context"
	"errors"
	"testing"
)

func TestSentinelErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if _, err := prepareAgent(Config{Generation: 4}); !errors.Is(err, ErrUnsupportedGeneration) {
		t.Fatalf("prepareAgent() error = %v", err)
	}
	if _, err := FetchWhistle(ctx, FetchOptions{Generation: 2}); !errors.Is(err, ErrUnsupportedGeneration) {
		t.Fatalf("FetchWhistle() error = %v", err)
	}
	flagged := Response{Type: ResponseCall, Validation: &Validation{Negation: true}}
	if err := ValidateResponse(flagged); !errors.Is(err, ErrValidation) {
		t.Fatalf("ValidateResponse() error = %v", err)
	}

	fake := &fakeNative{responses: [][]byte{
		[]byte(`{"type":"call","function_calls":[{"name":"x","arguments":{}}],"validation":{"ungrounded":["x.a"]}}`),
	}}
	a, r := newTestAgent(t, fake, Config{Generation: 2})
	if _, err := a.Run(ctx, "hello", 0, 0); !errors.Is(err, ErrValidation) {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := a.Embed(ctx, "hello"); !errors.Is(err, ErrEmbeddingUnsupported) {
		t.Fatalf("Embed() error = %v", err)
	}
	fake.completeCode, fake.responses = -3, [][]byte{[]byte("native failure")}
	_, err := a.Complete(ctx, "hello", 0)
	var native *NativeError
	if !errors.As(err, &native) || native.Operation != "complete" || native.Code != -3 || native.Detail != "native failure" {
		t.Fatalf("Complete() error = %#v", err)
	}
	if err.Error() != "needle: complete failed with code -3: native failure" {
		t.Fatalf("Complete() error text = %q", err.Error())
	}

	w := &Whistle{runtime: r, buffer: make([]byte, DefaultSpeechBufferSize)}
	if _, err := w.Transcribe(ctx, nil, AudioOptions{}); !errors.Is(err, ErrSpeechUnsupported) {
		t.Fatalf("Transcribe() error = %v", err)
	}
}
