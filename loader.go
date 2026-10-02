//go:build (darwin && !ios) || (linux && !android) || windows

package needle

import (
	"fmt"

	"github.com/ebitengine/purego"
)

func registerNative(handle uintptr, symbol func(uintptr, string) (uintptr, error)) (*nativeAPI, error) {
	api := &nativeAPI{handle: handle}
	models, _ := symbol(handle, "needle_models")
	transcribe, _ := symbol(handle, "needle_transcribe")
	setAudio, _ := symbol(handle, "needle_set_audio")
	lastError, _ := symbol(handle, "needle_last_error")
	embed, _ := symbol(handle, "needle_embed")
	audioABI := models != 0 || transcribe != 0 || setAudio != 0
	if audioABI {
		// These symbols were introduced together with the six-argument completion ABI.
		for _, capability := range []struct {
			name    string
			address uintptr
		}{
			{"needle_models", models},
			{"needle_transcribe", transcribe},
			{"needle_set_audio", setAudio},
			{"needle_last_error", lastError},
		} {
			if capability.address == 0 {
				return nil, fmt.Errorf("needle: unsupported native ABI: missing %s", capability.name)
			}
		}
		if embed == 0 {
			return nil, fmt.Errorf("needle: unsupported native ABI: missing needle_embed")
		}
	}
	if lastError != 0 {
		purego.RegisterFunc(&api.lastError, lastError)
	}
	complete := any(&api.complete)
	if audioABI {
		complete = &api.completeAudio
	}
	for _, binding := range []struct {
		name   string
		target any
	}{
		{"needle_init", &api.init},
		{"needle_complete", complete},
		{"needle_reset", &api.reset},
		{"needle_load", &api.load},
	} {
		address, err := symbol(handle, binding.name)
		if err != nil {
			return nil, fmt.Errorf("needle: resolve %s: %w", binding.name, err)
		}
		if address == 0 {
			return nil, fmt.Errorf("needle: resolve %s: null symbol", binding.name)
		}
		purego.RegisterFunc(binding.target, address)
	}
	if audioABI {
		purego.RegisterFunc(&api.transcribe, transcribe)
		purego.RegisterFunc(&api.setAudio, setAudio)
		api.complete = func(input *byte, tokens int32, output []byte, capacity int32) int32 {
			return api.completeAudio(input, nil, 0, tokens, output, capacity)
		}
	}
	if embed != 0 {
		if audioABI {
			var embedAudio func(*byte, *float32, int32, []float32, int32) int32
			purego.RegisterFunc(&embedAudio, embed)
			api.embed = func(input *byte, output []float32, capacity int32) int32 {
				return embedAudio(input, nil, 0, output, capacity)
			}
		} else {
			purego.RegisterFunc(&api.embed, embed)
		}
	}
	return api, nil
}
