//go:build windows

package needle

import (
	"fmt"
	"syscall"

	"github.com/ebitengine/purego"
)

func loadNative(path string) (*nativeAPI, error) {
	handle, err := syscall.LoadLibrary(path)
	if err != nil {
		return nil, fmt.Errorf("needle: load native library: %w", err)
	}
	resolve := func(handle uintptr, name string) (uintptr, error) {
		return syscall.GetProcAddress(syscall.Handle(handle), name)
	}
	api, err := registerNativeWindows(uintptr(handle), resolve)
	if err != nil {
		_ = syscall.FreeLibrary(handle)
		return nil, err
	}
	return api, nil
}

func registerNativeWindows(handle uintptr, symbol func(uintptr, string) (uintptr, error)) (*nativeAPI, error) {
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
