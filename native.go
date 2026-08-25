package needle

import (
	"errors"
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
