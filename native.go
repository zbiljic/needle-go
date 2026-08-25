package needle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ErrUnsupportedPlatform indicates that no PureGo shared-library backend is
// available for the requested target.
var ErrUnsupportedPlatform = errors.New("needle: unsupported platform")

type nativeAPI struct {
	handle   uintptr
	init     func(*byte, *byte, *byte) int32
	complete func(*byte, int32, []byte, int32) int32
	reset    func()
	load     func([]byte, uint64) int32
}

type processRuntime struct {
	mu sync.Mutex

	api           *nativeAPI
	libraryPath   string
	active        *agent
	activeWeights string
	activeBlob    []byte
}

var defaultRuntime processRuntime

func (r *processRuntime) ensureLibraryLocked(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("needle: resolve library path: %w", err)
	}
	if r.api != nil {
		if absolute != r.libraryPath {
			return fmt.Errorf("needle: engine already loaded from %s", r.libraryPath)
		}
		return nil
	}
	api, err := loadNative(absolute)
	if err != nil {
		return err
	}
	r.api = api
	r.libraryPath = absolute
	return nil
}

func (r *processRuntime) bindLocked(a *agent) error {
	if r.api == nil {
		return errors.New("needle: engine is not loaded")
	}
	if r.active == a {
		return nil
	}
	if a.weightsPath == "" && r.activeWeights != "" {
		return fmt.Errorf(
			"needle: tuned weights %s are loaded and cannot be unloaded; use a separate process for the base model",
			r.activeWeights,
		)
	}
	if a.weightsPath != "" && a.weightsPath != r.activeWeights {
		blob, err := os.ReadFile(a.weightsPath)
		if err != nil {
			return fmt.Errorf("needle: read weights: %w", err)
		}
		if len(blob) == 0 {
			return errors.New("needle: weights file is empty")
		}
		if code := r.api.load(blob, uint64(len(blob))); code != 0 {
			return fmt.Errorf("needle: load weights failed with code %d", code)
		}
		r.activeBlob = blob
		r.activeWeights = a.weightsPath
	}
	if code := r.api.init(bytePointer(a.system), bytePointer(a.tools), bytePointer(a.toolIndexPath)); code < 0 {
		r.active = nil
		return fmt.Errorf("needle: initialize failed with code %d", code)
	}
	r.active = a
	return nil
}
