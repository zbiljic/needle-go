package needle

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	needle2WeightsTag = 0x05e12a83
	needle3WeightsTag = 0x05e12a84
)

// ErrUnsupportedPlatform indicates that no PureGo shared-library backend is
// available for the requested target.
var ErrUnsupportedPlatform = errors.New("needle: unsupported platform")

type nativeAPI struct {
	handle        uintptr
	init          func(*byte, *byte, *byte) int32
	complete      func(*byte, int32, []byte, int32) int32
	completeAudio func(*byte, *float32, int32, int32, []byte, int32) int32
	embed         func(*byte, []float32, int32) int32
	lastError     func() string
	reset         func()
	load          func([]byte, uint64) int32
}

// failure reads the process-global diagnostic before another native call can replace it.
// The caller must hold the runtime mutex.
func (api *nativeAPI) failure(operation string, code int32, fallback string) error {
	if api.lastError != nil {
		if detail := api.lastError(); strings.TrimSpace(detail) != "" {
			fallback = detail
		}
	}
	detail := strings.TrimSpace(strings.ToValidUTF8(fallback, "�"))
	if detail != "" {
		return fmt.Errorf("needle: %s failed with code %d: %s", operation, code, detail)
	}
	return fmt.Errorf("needle: %s failed with code %d", operation, code)
}

type processRuntime struct {
	mu sync.Mutex

	api           *nativeAPI
	libraryPath   string
	active        *agent
	activeWeights string
	activeBlob    []byte
}

var runtimes = map[int]*processRuntime{2: {}, 3: {}}

// WeightsGeneration reads a .cact header and returns its model generation.
// It does not validate the rest of the archive.
func WeightsGeneration(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("needle: read weights: %w", err)
	}
	defer file.Close()
	var header [4]byte
	n, err := io.ReadFull(file, header[:])
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return 0, fmt.Errorf("needle: read weights: %w", err)
	}
	return weightsGeneration(header[:n])
}

func weightsGeneration(blob []byte) (int, error) {
	if len(blob) == 0 {
		return 0, errors.New("needle: weights file is empty")
	}
	if len(blob) < 4 {
		return 0, errors.New("needle: weights header is truncated")
	}
	switch tag := binary.LittleEndian.Uint32(blob[:4]); tag {
	case needle2WeightsTag:
		return 2, nil
	case needle3WeightsTag:
		return 3, nil
	default:
		return 0, fmt.Errorf("needle: unknown weights header 0x%08x", tag)
	}
}

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
		generation, err := weightsGeneration(blob)
		if err != nil {
			return err
		}
		if generation != a.generation {
			return fmt.Errorf(
				"needle: weights generation changed: got %d, want %d", generation, a.generation,
			)
		}
		if code := r.api.load(blob, uint64(len(blob))); code < 0 {
			return r.api.failure("load weights", code, "")
		}
		r.activeBlob = blob
		r.activeWeights = a.weightsPath
		r.active = nil
	}
	a.calibrated = !a.tuned || (a.generation == 3 && confidenceHeadPresent(r.activeBlob))
	if code := r.api.init(bytePointer(a.system), bytePointer(a.tools), bytePointer(a.toolIndexPath)); code < 0 {
		r.active = nil
		return r.api.failure("initialize", code, "")
	}
	r.active = a
	return nil
}
