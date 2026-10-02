package needle

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestEmbed(t *testing.T) {
	t.Parallel()
	fake := &fakeNative{}
	a, runtime := newTestAgent(t, fake, Config{Stateless: true})
	calls := 0
	runtime.api.embed = func(input *byte, output []float32, capacity int32) int32 {
		calls++
		if readCString(input) != "hello" || int(capacity) != len(output) {
			t.Fatalf("embedding arguments: input=%q capacity=%d output=%v", readCString(input), capacity, output)
		}
		copy(output, []float32{0.25, -0.75})
		return 2
	}
	vector, err := a.Embed(context.Background(), "hello")
	if err != nil || !reflect.DeepEqual(vector, []float32{0.25, -0.75}) || calls != 2 {
		t.Fatalf("Embed() = %v, %v; calls=%d", vector, err, calls)
	}
	if fake.resets != 0 || len(fake.systems) != 1 {
		t.Fatalf("Embed changed conversation: resets=%d initializations=%d", fake.resets, len(fake.systems))
	}
	// Rebinding follows the same rules as Complete; it must select this agent.
	runtime.active = nil
	if _, err := a.Embed(context.Background(), "hello"); err != nil || runtime.active != a || len(fake.systems) != 2 {
		t.Fatalf("Embed did not bind agent: %v", err)
	}
}

func TestEmbedErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, want string
		size, code int32
	}{
		{"size failure", "embed size failed with code -1: native detail", -1, 0},
		{"zero size", "invalid embedding size 0", 0, 0},
		{"oversized", "invalid embedding size", maxEmbeddingFloats + 1, 0},
		{"overflowing", "invalid embedding size", math.MaxInt32, 0},
		{"output failure", "embed failed with code -2: native detail", 2, -2},
		{"short output", "embedding length 1, want 2", 2, 1},
		{"long output", "embedding length 3, want 2", 2, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, runtime := newTestAgent(t, &fakeNative{}, Config{})
			calls := 0
			runtime.api.lastError = func() string { return "native detail" }
			runtime.api.embed = func(_ *byte, output []float32, _ int32) int32 {
				calls++
				if output == nil {
					return test.size
				}
				return test.code
			}
			vector, err := a.Embed(context.Background(), "hello")
			if vector != nil || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Embed() = %v, %v; want %q", vector, err, test.want)
			}
			if test.size <= 0 || test.size > maxEmbeddingFloats {
				if calls != 1 {
					t.Fatalf("invalid size triggered %d calls", calls)
				}
			}
		})
	}
}

func TestEmbedUnavailableAndInvalidInput(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, input, want string
		generation        int
	}{
		{"Needle 2", "hello", "embeddings require Needle 3", 2},
		{"missing symbol", "hello", "engine does not support embeddings", 3},
		{"NUL", "hello\x00world", "input contains NUL", 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, _ := newTestAgent(t, &fakeNative{}, Config{Generation: test.generation})
			if vector, err := a.Embed(context.Background(), test.input); vector != nil || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Embed() = %v, %v; want %q", vector, err, test.want)
			}
		})
	}
}

func TestEmbedCancellationAndBindFailure(t *testing.T) {
	t.Parallel()
	a, runtime := newTestAgent(t, &fakeNative{}, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Embed(ctx, "hello"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Embed() = %v", err)
	}
	// Cancel during size lookup: do not continue into expensive native inference.
	ctx, cancel = context.WithCancel(context.Background())
	calls := 0
	runtime.api.embed = func(*byte, []float32, int32) int32 {
		calls++
		cancel()
		return 2
	}
	if _, err := a.Embed(ctx, "hello"); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("Embed canceled after size query: err=%v calls=%d", err, calls)
	}
	runtime.active = nil
	runtime.api.init = func(*byte, *byte, *byte) int32 { return -3 }
	if _, err := a.Embed(context.Background(), "hello"); err == nil || !strings.Contains(err.Error(), "initialize failed with code -3") || calls != 1 {
		t.Fatalf("Embed after bind failure: err=%v calls=%d", err, calls)
	}
}
