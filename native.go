package needle

import (
	"errors"
)

// ErrUnsupportedPlatform indicates that no PureGo shared-library backend is
// available for the requested target.
var ErrUnsupportedPlatform = errors.New("needle: unsupported platform")
