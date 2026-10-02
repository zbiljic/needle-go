//go:build (darwin && !ios) || (linux && !android) || windows

package needle

import (
	"errors"
	"strings"
	"testing"
	"unsafe"

	"github.com/ebitengine/purego"
)

func TestRegisterNativeABI(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "audio"}[modern], func(t *testing.T) {
			calls := 0
			var gotInput string
			var gotTokens, gotCapacity, gotSamples int32
			var gotPCM *float32
			transcriptions, audioSettings := 0, 0
			embedCalls := 0
			embed := func(input *byte, output *float32, capacity int32) int32 {
				embedCalls++
				if readCString(input) != "hello" {
					t.Error("wrong embedding input")
				}
				if output != nil {
					if capacity != 2 {
						t.Errorf("embedding capacity = %d", capacity)
					}
					copy(unsafe.Slice(output, int(capacity)), []float32{0.25, -0.75})
				} else if capacity != 0 {
					t.Error("size query has nonzero capacity")
				}
				return 2
			}
			complete := func(input *byte, tokens int32, output *byte, capacity int32) int32 {
				calls++
				gotInput, gotTokens, gotCapacity = readCString(input), tokens, capacity
				copy(unsafe.Slice(output, int(capacity)), "response\x00")
				return 7
			}
			symbols := map[string]uintptr{
				"needle_init":     purego.NewCallback(func(*byte, *byte, *byte) int32 { return 0 }),
				"needle_complete": purego.NewCallback(complete),
				"needle_reset":    purego.NewCallback(func() {}),
				"needle_load":     purego.NewCallback(func(*byte, uint64) int32 { return 0 }),
				"needle_embed":    purego.NewCallback(embed),
			}
			if modern {
				symbols["needle_complete"] = purego.NewCallback(func(input *byte, pcm *float32, samples, tokens int32, output *byte, capacity int32) int32 {
					gotPCM, gotSamples = pcm, samples
					return complete(input, tokens, output, capacity)
				})
				symbols["needle_models"] = 1 // Probed, never called during registration.
				symbols["needle_transcribe"] = purego.NewCallback(func(pcm *float32, samples int32, language, keywords *byte, timestamps int32, output *byte, capacity int32) int32 {
					transcriptions++
					if pcm == nil || samples != 2 || *pcm != 0.25 || readCString(language) != "en" || readCString(keywords) != "Paris\nAda" || timestamps != 1 || capacity != 32 {
						t.Error("incorrect seven-argument transcription ABI")
					}
					copy(unsafe.Slice(output, capacity), "speech\x00")
					return 17
				})
				symbols["needle_set_audio"] = purego.NewCallback(func(language, keywords *byte, timestamps int32) {
					audioSettings++
					if audioSettings == 1 && (readCString(language) != "en" || readCString(keywords) != "Paris\nAda" || timestamps != 1) {
						t.Error("incorrect three-argument audio options ABI")
					}
					if audioSettings == 2 && (language != nil || keywords != nil || timestamps != 0) {
						t.Error("audio options were not cleared")
					}
				})
				symbols["needle_embed"] = purego.NewCallback(func(input *byte, pcm *float32, samples int32, output *float32, capacity int32) int32 {
					if pcm != nil || samples != 0 {
						t.Error("text embedding received audio arguments")
					}
					return embed(input, output, capacity)
				})
				diagnostic := []byte("native detail\x00")
				symbols["needle_last_error"] = purego.NewCallback(func() *byte { return &diagnostic[0] })
			}
			resolve := func(_ uintptr, name string) (uintptr, error) {
				if address, ok := symbols[name]; ok {
					return address, nil
				}
				return 0, errors.New("symbol absent")
			}
			api, err := registerNative(42, resolve)
			if err != nil {
				t.Fatal(err)
			}
			input, _ := cString("input", "hello", false)
			output := make([]byte, 32)
			if code := api.complete(&input[0], 123, output, int32(len(output))); code != 7 {
				t.Fatalf("complete code = %d", code)
			}
			if calls != 1 || gotInput != "hello" || gotTokens != 123 || gotCapacity != 32 || gotPCM != nil || gotSamples != 0 || string(output[:8]) != "response" {
				t.Fatalf("ABI arguments: calls=%d input=%q tokens=%d capacity=%d pcm=%p samples=%d output=%q", calls, gotInput, gotTokens, gotCapacity, gotPCM, gotSamples, output)
			}
			if api.handle != 42 || (api.completeAudio != nil) != modern || (api.lastError != nil) != modern {
				t.Fatal("wrong native capabilities")
			}
			if modern && api.lastError() != "native detail" {
				t.Fatal("native diagnostic was not copied")
			}
			if modern {
				language, _ := cString("language", "en", false)
				keywords, _ := cString("keywords", "Paris\nAda", false)
				pcm := []float32{0.25, -0.5}
				api.setAudio(&language[0], &keywords[0], 1)
				if code := api.transcribe(&pcm[0], 2, &language[0], &keywords[0], 1, output, 32); code != 17 || string(output[:6]) != "speech" {
					t.Fatalf("transcription ABI: code=%d output=%q", code, output)
				}
				api.setAudio(nil, nil, 0)
				if transcriptions != 1 || audioSettings != 2 {
					t.Fatalf("transcriptions=%d audio settings=%d", transcriptions, audioSettings)
				}
			}
			vector := make([]float32, 2)
			if api.embed(&input[0], nil, 0) != 2 || api.embed(&input[0], vector, 2) != 2 || embedCalls != 2 || vector[0] != 0.25 || vector[1] != -0.75 {
				t.Fatalf("embedding ABI: calls=%d output=%v", embedCalls, vector)
			}
			names := make([]string, 0, len(symbols))
			for name := range symbols {
				names = append(names, name)
			}
			for _, name := range names {
				address := symbols[name]
				if name == "needle_embed" && !modern {
					delete(symbols, name)
					legacy, err := registerNative(42, resolve)
					if err != nil || legacy.embed != nil {
						t.Fatalf("legacy engine without embeddings: api=%+v err=%v", legacy, err)
					}
					symbols[name] = address
					continue
				}
				symbols[name] = 0
				if _, err := registerNative(42, resolve); err == nil || !strings.Contains(err.Error(), name) {
					t.Fatalf("null %s: %v", name, err)
				}
				delete(symbols, name)
				if _, err := registerNative(42, resolve); err == nil || !strings.Contains(err.Error(), name) {
					t.Fatalf("missing %s: %v", name, err)
				}
				symbols[name] = address
			}
		})
	}
}
