package needle

import (
	"context"
	"fmt"
)

// maxEmbeddingFloats caps text vectors at 4 MiB; raise it if a future model needs wider features.
const maxEmbeddingFloats = 1 << 20

// Embed returns text features from Needle 3 without resetting the conversation.
// Engines without embedding support return an error.
func (a *Agent) Embed(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.generation < 3 {
		return nil, fmt.Errorf("%w: embeddings require Needle 3", ErrEmbeddingUnsupported)
	}
	input, err := cString("input", text, false)
	if err != nil {
		return nil, err
	}

	a.runtime.mu.Lock()
	defer a.runtime.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := a.runtime.bindLocked(a); err != nil {
		return nil, err
	}
	api := a.runtime.api
	if api.embed == nil {
		return nil, fmt.Errorf("%w: engine does not support embeddings", ErrEmbeddingUnsupported)
	}
	size := api.embed(bytePointer(input), nil, 0)
	if size < 0 {
		return nil, api.failure("embed size", size, "")
	}
	if size == 0 || size > maxEmbeddingFloats {
		return nil, fmt.Errorf("needle: invalid embedding size %d", size)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	output := make([]float32, int(size))
	code := api.embed(bytePointer(input), output, size)
	if code < 0 {
		return nil, api.failure("embed", code, "")
	}
	if code != size {
		return nil, fmt.Errorf("needle: embedding length %d, want %d", code, size)
	}
	return output, nil
}
