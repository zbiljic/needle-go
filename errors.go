package needle

import (
	"errors"
	"fmt"
)

// Sentinel errors for errors.Is. Returned errors wrap them with detail.
var (
	// ErrUnsupportedPlatform indicates that no shared-library backend or engine
	// build exists for the target.
	ErrUnsupportedPlatform = errors.New("needle: unsupported platform")
	// ErrEngineNotFound indicates that a cached engine library is unavailable.
	ErrEngineNotFound = errors.New("needle: engine library not found")
	// ErrUnsupportedGeneration indicates a generation other than 2 or 3, or a
	// feature the selected generation lacks.
	ErrUnsupportedGeneration = errors.New("needle: unsupported generation")
	// ErrUnsupportedABI indicates a native library with an incomplete
	// capability set.
	ErrUnsupportedABI = errors.New("needle: unsupported native ABI")
	// ErrEmbeddingUnsupported indicates that the model or engine has no
	// embeddings.
	ErrEmbeddingUnsupported = errors.New("needle: embeddings unavailable")
	// ErrSpeechUnsupported indicates that the engine has no speech ABI.
	ErrSpeechUnsupported = errors.New("needle: speech unavailable")
	// ErrValidation is wrapped by ValidateResponse, and by Run, when the engine
	// flags a response.
	ErrValidation = errors.New("needle: response validation")
	// ErrClosed is returned by an Agent or Whistle after Close.
	ErrClosed = errors.New("needle: closed")
)

// NativeError reports a failed engine call. Detail carries the engine's
// diagnostic when one is available.
type NativeError struct {
	Operation string
	Code      int32
	Detail    string
}

func (e *NativeError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("needle: %s failed with code %d: %s", e.Operation, e.Code, e.Detail)
	}
	return fmt.Sprintf("needle: %s failed with code %d", e.Operation, e.Code)
}
