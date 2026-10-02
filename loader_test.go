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
			}
			if modern {
				symbols["needle_complete"] = purego.NewCallback(func(input *byte, pcm *float32, samples, tokens int32, output *byte, capacity int32) int32 {
					gotPCM, gotSamples = pcm, samples
					return complete(input, tokens, output, capacity)
				})
				for _, name := range []string{"needle_models", "needle_transcribe", "needle_set_audio", "needle_embed"} {
					symbols[name] = 1 // Capabilities are probed, never called during registration.
				}
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
			names := make([]string, 0, len(symbols))
			for name := range symbols {
				names = append(names, name)
			}
			for _, name := range names {
				address := symbols[name]
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
