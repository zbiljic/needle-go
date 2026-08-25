//go:build (!darwin && !linux && !windows) || android || ios

package needle

import (
	"fmt"
	"runtime"
)

func loadNative(string) (*nativeAPI, error) {
	return nil, fmt.Errorf("%w: %s/%s", ErrUnsupportedPlatform, runtime.GOOS, runtime.GOARCH)
}
