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

func registerNative(handle uintptr, symbol func(uintptr, string) (uintptr, error)) (*nativeAPI, error) {
	api := &nativeAPI{handle: handle}
	bindings := []struct {
		name   string
		target any
	}{
		{name: "needle_init", target: &api.init},
		{name: "needle_complete", target: &api.complete},
		{name: "needle_reset", target: &api.reset},
		{name: "needle_load", target: &api.load},
	}
	for _, binding := range bindings {
		address, err := symbol(handle, binding.name)
		if err != nil {
			return nil, fmt.Errorf("needle: resolve %s: %w", binding.name, err)
		}
		purego.RegisterFunc(binding.target, address)
	}
	return api, nil
}
