//go:build (darwin && !ios) || (linux && !android)

package needle

import (
	"fmt"

	"github.com/ebitengine/purego"
)

func loadNative(path string) (*nativeAPI, error) {
	handle, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("needle: load native library: %w", err)
	}
	api, err := registerNative(handle, purego.Dlsym)
	if err != nil {
		_ = purego.Dlclose(handle)
		return nil, err
	}
	return api, nil
}
