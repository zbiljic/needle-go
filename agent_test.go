package needle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

type fakeNative struct {
	initCode     int32
	completeCode int32
	loadCode     int32
	responses    [][]byte
	inputs       []string
	tokens       []int32
	systems      []string
	tools        []string
	indexes      []*string
	loaded       []byte
	loads        int
	resets       int
}

func (f *fakeNative) api() *nativeAPI {
	return &nativeAPI{
		init: func(system, tools, index *byte) int32 {
			f.systems = append(f.systems, readCString(system))
			f.tools = append(f.tools, readCString(tools))
			if index == nil {
				f.indexes = append(f.indexes, nil)
			} else {
				value := readCString(index)
				f.indexes = append(f.indexes, &value)
			}
			return f.initCode
		},
		complete: func(input *byte, tokens int32, output []byte, _ int32) int32 {
			f.inputs = append(f.inputs, readCString(input))
			f.tokens = append(f.tokens, tokens)
			if f.completeCode < 0 {
				return f.completeCode
			}
			if len(f.responses) > 0 {
				copy(output, f.responses[0])
				f.responses = f.responses[1:]
			}
			return f.completeCode
		},
		reset: func() { f.resets++ },
		load: func(blob []byte, size uint64) int32 {
			f.loads++
			f.loaded = append([]byte(nil), blob[:size]...)
			return f.loadCode
		},
	}
}

func newTestAgent(t *testing.T, fake *fakeNative, config Config) (*agent, *processRuntime) {
	t.Helper()
	prepared, err := prepareAgent(config)
	if err != nil {
		t.Fatalf("prepareAgent() error = %v", err)
	}
	runtime := &processRuntime{api: fake.api(), libraryPath: "test"}
	prepared.runtime = runtime
	runtime.mu.Lock()
	err = runtime.bindLocked(prepared)
	runtime.mu.Unlock()
	if err != nil {
		t.Fatalf("bindLocked() error = %v", err)
	}
	return prepared, runtime
}

func TestPrepareAgentConfiguresNativeSession(t *testing.T) {
	t.Parallel()

	fake := &fakeNative{}
	tool := Tool{
		Schema:  ToolSchema{Name: "weather", Description: "Get the weather."},
		Handler: func(context.Context, json.RawMessage) (any, error) { return nil, nil },
	}
	agent, _ := newTestAgent(t, fake, Config{
		Tools:         []Tool{tool},
		System:        "device: phone",
		ToolIndexPath: "tools.idx",
	})
	if agent == nil {
		t.Fatal("agent is nil")
	}
	if fake.systems[0] != "device: phone" {
		t.Fatalf("system = %q", fake.systems[0])
	}
	if fake.indexes[0] == nil || *fake.indexes[0] != "tools.idx" {
		t.Fatalf("tool index = %#v", fake.indexes[0])
	}
	var schemas []ToolSchema
	if err := json.Unmarshal([]byte(fake.tools[0]), &schemas); err != nil {
		t.Fatalf("tools JSON error = %v", err)
	}
	if len(schemas) != 1 || schemas[0].Name != "weather" {
		t.Fatalf("tools = %#v", schemas)
	}
	if schemas[0].Parameters["type"] != "object" {
		t.Fatalf("default parameters = %#v", schemas[0].Parameters)
	}
}

func TestPrepareAgentValidatesConfig(t *testing.T) {
	t.Parallel()

	handler := func(context.Context, json.RawMessage) (any, error) { return nil, nil }
	tests := []struct {
		name   string
		config Config
	}{
		{name: "empty tool name", config: Config{Tools: []Tool{{Handler: handler}}}},
		{name: "duplicate tool", config: Config{Tools: []Tool{
			{Schema: ToolSchema{Name: "one"}},
			{Schema: ToolSchema{Name: "one"}},
		}}},
		{name: "invalid schema", config: Config{Tools: []Tool{{
			Schema: ToolSchema{Name: "bad", Parameters: map[string]any{"invalid": make(chan int)}},
		}}}},
		{name: "small buffer", config: Config{BufferSize: 1}},
		{name: "large buffer", config: Config{BufferSize: int(^uint32(0))}},
		{name: "NUL system", config: Config{System: "bad\x00value"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := prepareAgent(test.config); err == nil {
				t.Fatal("prepareAgent() error = nil")
			}
		})
	}
}

func TestCompleteDecodesResponseAndDefaultsTokenLimit(t *testing.T) {
	t.Parallel()

	fake := &fakeNative{responses: [][]byte{[]byte(`{"type":"call","success":true,"function_calls":[{"name":"weather","arguments":{"city":"Paris"}}],"confidence":0.94}`)}}
	agent, _ := newTestAgent(t, fake, Config{})
	response, err := agent.Complete(context.Background(), "weather in Paris", 0)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if response.Type != ResponseCall || len(response.FunctionCalls) != 1 {
		t.Fatalf("Complete() response = %#v", response)
	}
	if fake.tokens[0] != DefaultMaxNewTokens {
		t.Fatalf("max tokens = %d, want %d", fake.tokens[0], DefaultMaxNewTokens)
	}
}

func TestCompleteErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		fake       *fakeNative
		config     Config
		input      string
		maxTokens  int
		cancel     bool
		wantPhrase string
	}{
		{name: "native failure", fake: &fakeNative{completeCode: -3}, wantPhrase: "code -3"},
		{name: "invalid JSON", fake: &fakeNative{responses: [][]byte{[]byte("invalid")}}, wantPhrase: "decode response"},
		{name: "missing type", fake: &fakeNative{responses: [][]byte{[]byte(`{"success":true}`)}}, wantPhrase: "response type"},
		{name: "short buffer", fake: &fakeNative{responses: [][]byte{[]byte(`12345678`)}}, config: Config{BufferSize: 8}, wantPhrase: "exceeds buffer"},
		{name: "NUL input", fake: &fakeNative{}, input: "bad\x00input", wantPhrase: "contains NUL"},
		{name: "negative tokens", fake: &fakeNative{}, maxTokens: -1, wantPhrase: "invalid max new tokens"},
		{name: "canceled", fake: &fakeNative{}, cancel: true, wantPhrase: "context canceled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			agent, _ := newTestAgent(t, test.fake, test.config)
			ctx := context.Background()
			if test.cancel {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			_, err := agent.Complete(ctx, test.input, test.maxTokens)
			if err == nil || !strings.Contains(err.Error(), test.wantPhrase) {
				t.Fatalf("Complete() error = %v, want %q", err, test.wantPhrase)
			}
		})
	}
}

func TestRunExecutesToolsUntilResponse(t *testing.T) {
	t.Parallel()

	fake := &fakeNative{responses: [][]byte{
		[]byte(`{"type":"call","function_calls":[{"name":"weather","arguments":{"city":"Lagos"}}]}`),
		[]byte(`{"type":"respond","function_calls":[]}`),
	}}
	tool := Tool{
		Schema: ToolSchema{Name: "weather"},
		Handler: func(_ context.Context, arguments json.RawMessage) (any, error) {
			var input struct {
				City string `json:"city"`
			}
			if err := json.Unmarshal(arguments, &input); err != nil {
				return nil, err
			}
			return map[string]any{"city": input.City, "temp_c": 27}, nil
		},
	}
	agent, _ := newTestAgent(t, fake, Config{Tools: []Tool{tool}})
	response, err := agent.Run(context.Background(), "weather in Lagos", 0, 0)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if response.Type != ResponseRespond || len(response.Results) != 1 {
		t.Fatalf("Run() response = %#v", response)
	}
	if got, want := fake.inputs[1], `[{"city":"Lagos","temp_c":27}]`; got != want {
		t.Fatalf("tool result input = %s, want %s", got, want)
	}
}

func TestRunReportsToolErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		callName  string
		tool      Tool
		wantInput string
		wantError string
	}{
		{
			name:      "unknown tool",
			callName:  "missing",
			tool:      Tool{Schema: ToolSchema{Name: "known"}},
			wantInput: `[{"error":"unknown tool: missing"}]`,
			wantError: "unknown tool: missing",
		},
		{
			name:     "handler failure",
			callName: "known",
			tool: Tool{Schema: ToolSchema{Name: "known"}, Handler: func(context.Context, json.RawMessage) (any, error) {
				return nil, errors.New("offline")
			}},
			wantInput: `[{"error":"offline"}]`,
			wantError: "offline",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			call, _ := json.Marshal(map[string]any{
				"type":           "call",
				"function_calls": []any{map[string]any{"name": test.callName, "arguments": map[string]any{}}},
			})
			fake := &fakeNative{responses: [][]byte{call, []byte(`{"type":"respond"}`)}}
			agent, _ := newTestAgent(t, fake, Config{Tools: []Tool{test.tool}})
			response, err := agent.Run(context.Background(), "query", 1, 1)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if fake.inputs[1] != test.wantInput {
				t.Fatalf("tool error input = %s, want %s", fake.inputs[1], test.wantInput)
			}
			wantResult := []any{map[string]string{"error": test.wantError}}
			if !reflect.DeepEqual(response.Results, wantResult) {
				t.Fatalf("Run() results = %#v, want %#v", response.Results, wantResult)
			}
		})
	}
}

func TestRunValidation(t *testing.T) {
	t.Parallel()

	call := func(name, arguments string, validation string) []byte {
		return []byte(`{"type":"call","function_calls":[{"name":"` + name +
			`","arguments":` + arguments + `}]` + validation + `}`)
	}
	validCall := call("tool", `{}`, "")
	tests := []struct {
		name           string
		responses      [][]byte
		maxSteps       int
		wantError      string
		wantHandlers   int
		wantCompletes  int
		wantQueued     int
		wantResults    int
		wantValidation *Validation
	}{
		{
			name:          "nil validation",
			responses:     [][]byte{validCall, []byte(`{"type":"respond"}`)},
			maxSteps:      1,
			wantHandlers:  1,
			wantCompletes: 2,
			wantResults:   1,
		},
		{
			name: "negation",
			responses: [][]byte{
				call("tool", `{}`, `,"confidence":0.933,"validation":{"negation":true}`),
			},
			wantError:      "negation",
			wantCompletes:  1,
			wantValidation: &Validation{Negation: true},
		},
		{
			name: "ungrounded numeric formatted source",
			responses: [][]byte{
				call("tool", `{"total":1200}`, `,"validation":{"ungrounded":["invoice.total"]}`),
			},
			wantError:      "invoice.total",
			wantCompletes:  1,
			wantValidation: &Validation{Ungrounded: []string{"invoice.total"}},
		},
		{
			name: "unrelated matching number in reasoning",
			responses: [][]byte{
				call("tool", `{"total":1200}`,
					`,"reasoning":"order ID 1200","validation":{"ungrounded":["invoice.total"]}`),
			},
			wantError:      "invoice.total",
			wantCompletes:  1,
			wantValidation: &Validation{Ungrounded: []string{"invoice.total"}},
		},
		{
			name: "unknown field on empty calls",
			responses: [][]byte{
				[]byte(`{"type":"call","function_calls":[],"validation":{"ungrounded":["unknown.path"]}}`),
			},
			wantError:      "unknown.path",
			wantCompletes:  1,
			wantValidation: &Validation{Ungrounded: []string{"unknown.path"}},
		},
		{
			name: "warning on response envelope",
			responses: [][]byte{
				[]byte(`{"type":"respond","validation":{"negation":true}}`),
			},
			wantError:      "negation",
			wantCompletes:  1,
			wantValidation: &Validation{Negation: true},
		},
		{
			name: "two calls whole turn rejected",
			responses: [][]byte{
				[]byte(`{"type":"call","function_calls":[{"name":"tool","arguments":{}},{"name":"tool","arguments":{}}],"validation":{"ungrounded":["tool.value"]}}`),
			},
			wantError:      "tool.value",
			wantCompletes:  1,
			wantValidation: &Validation{Ungrounded: []string{"tool.value"}},
		},
		{
			name: "prior results retained and retry untouched",
			responses: [][]byte{
				validCall,
				call("tool", `{}`, `,"validation":{"negation":true}`),
				[]byte(`{"type":"respond"}`),
			},
			maxSteps:       2,
			wantError:      "negation",
			wantHandlers:   1,
			wantCompletes:  2,
			wantQueued:     1,
			wantResults:    1,
			wantValidation: &Validation{Negation: true},
		},
		{
			name: "final completion after max steps rejected",
			responses: [][]byte{
				validCall,
				[]byte(`{"type":"respond","validation":{"ungrounded":["final.answer"]}}`),
			},
			maxSteps:       1,
			wantError:      "final.answer",
			wantHandlers:   1,
			wantCompletes:  2,
			wantResults:    1,
			wantValidation: &Validation{Ungrounded: []string{"final.answer"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeNative{responses: append([][]byte(nil), test.responses...)}
			handlers := 0
			agent, _ := newTestAgent(t, fake, Config{
				Tools: []Tool{{
					Schema: ToolSchema{Name: "tool"},
					Handler: func(context.Context, json.RawMessage) (any, error) {
						handlers++
						return "ok", nil
					},
				}},
			})
			response, err := agent.Run(
				context.Background(), "$1,200.00 and order ID 1200", test.maxSteps, 1,
			)
			if (test.wantError == "") != (err == nil) || err != nil &&
				(!strings.Contains(err.Error(), "needle: run:") ||
					!strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("Run() error = %v, want %q", err, test.wantError)
			}
			if handlers != test.wantHandlers || len(fake.inputs) != test.wantCompletes ||
				len(fake.responses) != test.wantQueued || len(response.Results) != test.wantResults {
				t.Fatalf("handlers=%d completes=%d queued=%d results=%#v",
					handlers, len(fake.inputs), len(fake.responses), response.Results)
			}
			if !reflect.DeepEqual(response.Validation, test.wantValidation) {
				t.Fatalf("validation = %#v, want %#v", response.Validation, test.wantValidation)
			}
			var wantResponse Response
			if err := json.Unmarshal(test.responses[test.wantCompletes-1], &wantResponse); err != nil {
				t.Fatal(err)
			}
			wantResponse.Results = make([]any, test.wantResults)
			for i := range wantResponse.Results {
				wantResponse.Results[i] = "ok"
			}
			if !reflect.DeepEqual(response, wantResponse) {
				t.Fatalf("Run() response = %#v, want %#v", response, wantResponse)
			}
		})
	}
}

func TestReset(t *testing.T) {
	t.Parallel()

	fake := &fakeNative{}
	agent, _ := newTestAgent(t, fake, Config{})
	if err := agent.Reset(context.Background()); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if fake.resets != 1 {
		t.Fatalf("reset calls = %d, want 1", fake.resets)
	}
}

func TestRuntimeRebindsAgents(t *testing.T) {
	t.Parallel()

	fake := &fakeNative{}
	runtime := &processRuntime{api: fake.api(), libraryPath: "test"}
	first, err := prepareAgent(Config{System: "user: first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareAgent(Config{System: "user: second"})
	if err != nil {
		t.Fatal(err)
	}
	first.runtime, second.runtime = runtime, runtime
	fake.responses = [][]byte{[]byte(`{"type":"respond"}`), []byte(`{"type":"respond"}`)}
	if _, err := first.Complete(context.Background(), "one", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Complete(context.Background(), "two", 1); err != nil {
		t.Fatal(err)
	}
	if got, want := fake.systems, []string{"user: first", "user: second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("initialized systems = %#v, want %#v", got, want)
	}
}

func TestRuntimeRetainsTunedWeights(t *testing.T) {
	t.Parallel()

	weightsPath := t.TempDir() + "/tuned.cact"
	weights := append([]byte{0x83, 0x2a, 0xe1, 0x05}, []byte("weights")...)
	if err := os.WriteFile(weightsPath, weights, 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeNative{}
	runtime := &processRuntime{api: fake.api(), libraryPath: "test"}
	tuned, err := prepareAgent(Config{WeightsPath: weightsPath})
	if err != nil {
		t.Fatal(err)
	}
	tuned.runtime = runtime
	runtime.mu.Lock()
	err = runtime.bindLocked(tuned)
	runtime.mu.Unlock()
	if err != nil {
		t.Fatalf("bind tuned agent: %v", err)
	}
	if !reflect.DeepEqual(fake.loaded, weights) || !reflect.DeepEqual(runtime.activeBlob, weights) {
		t.Fatalf("loaded weights = %q, retained = %q", fake.loaded, runtime.activeBlob)
	}
	if fake.loads != 1 {
		t.Fatalf("load calls = %d, want 1", fake.loads)
	}

	base, err := prepareAgent(Config{})
	if err != nil {
		t.Fatal(err)
	}
	base.runtime = runtime
	runtime.mu.Lock()
	err = runtime.bindLocked(base)
	runtime.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "cannot be unloaded") {
		t.Fatalf("bind base agent error = %v", err)
	}
}

func TestRejectWeightsGeneration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		blob      []byte
		missing   bool
		loadCode  int32
		wantError string
		wantLoads int
	}{
		{
			name:      "missing",
			missing:   true,
			wantError: "needle: read weights:",
		},
		{
			name:      "empty",
			blob:      []byte{},
			wantError: "needle: weights file is empty",
		},
		{
			name:      "one header byte",
			blob:      []byte{0x83},
			wantError: "needle: weights header is truncated",
		},
		{
			name:      "two header bytes",
			blob:      []byte{0x83, 0x2a},
			wantError: "needle: weights header is truncated",
		},
		{
			name:      "three header bytes",
			blob:      []byte{0x83, 0x2a, 0xe1},
			wantError: "needle: weights header is truncated",
		},
		{
			name:      "Needle 3",
			blob:      []byte{0x84, 0x2a, 0xe1, 0x05, 1},
			wantError: "needle: Needle 3 weights are unsupported; requires Needle 2",
		},
		{
			name:      "unknown",
			blob:      []byte{0, 0, 0, 1},
			wantError: "needle: unknown weights header 0x01000000",
		},
		{
			name:      "wrong byte order",
			blob:      []byte{0x05, 0xe1, 0x2a, 0x83},
			wantError: "needle: unknown weights header 0x832ae105",
		},
		{
			name:      "load failure",
			blob:      []byte{0x83, 0x2a, 0xe1, 0x05, 1},
			loadCode:  7,
			wantError: "needle: load weights failed with code 7",
			wantLoads: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			weightsPath := dir + "/tuned.cact"
			if !test.missing {
				if err := os.WriteFile(weightsPath, test.blob, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			prepared, err := prepareAgent(Config{WeightsPath: weightsPath})
			if err != nil {
				t.Fatal(err)
			}
			fake := &fakeNative{loadCode: test.loadCode}
			runtime := &processRuntime{api: fake.api(), libraryPath: "test"}
			prepared.runtime = runtime
			runtime.mu.Lock()
			err = runtime.bindLocked(prepared)
			runtime.mu.Unlock()
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("bindLocked() error = %v, want %q", err, test.wantError)
			}
			if test.missing && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("bindLocked() error = %v, want wrapped os.ErrNotExist", err)
			}
			if fake.loads != test.wantLoads || len(fake.systems) != 0 {
				t.Fatalf(
					"load calls = %d, init calls = %d, want %d, 0",
					fake.loads,
					len(fake.systems),
					test.wantLoads,
				)
			}
			if runtime.active != nil || runtime.activeWeights != "" || runtime.activeBlob != nil {
				t.Fatalf(
					"active state changed: agent=%p weights=%q blob=%q",
					runtime.active,
					runtime.activeWeights,
					runtime.activeBlob,
				)
			}
		})
	}

	t.Run("active tuned agent unchanged", func(t *testing.T) {
		dir := t.TempDir()
		validPath := dir + "/valid.cact"
		invalidPath := dir + "/invalid.cact"
		validBlob := []byte{0x83, 0x2a, 0xe1, 0x05, 1}
		if err := os.WriteFile(validPath, validBlob, 0o600); err != nil {
			t.Fatal(err)
		}
		invalidBlob := []byte{0x84, 0x2a, 0xe1, 0x05}
		if err := os.WriteFile(invalidPath, invalidBlob, 0o600); err != nil {
			t.Fatal(err)
		}
		fake := &fakeNative{}
		runtime := &processRuntime{api: fake.api(), libraryPath: "test"}
		active, err := prepareAgent(Config{WeightsPath: validPath})
		if err != nil {
			t.Fatal(err)
		}
		active.runtime = runtime
		runtime.mu.Lock()
		err = runtime.bindLocked(active)
		runtime.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		originalBlob := append([]byte(nil), runtime.activeBlob...)

		invalid, err := prepareAgent(Config{WeightsPath: invalidPath})
		if err != nil {
			t.Fatal(err)
		}
		invalid.runtime = runtime
		runtime.mu.Lock()
		err = runtime.bindLocked(invalid)
		runtime.mu.Unlock()
		if err == nil || !strings.Contains(err.Error(), "Needle 3 weights are unsupported") {
			t.Fatalf("bindLocked() error = %v", err)
		}
		if runtime.active != active ||
			runtime.activeWeights != validPath ||
			!reflect.DeepEqual(runtime.activeBlob, originalBlob) {
			t.Fatalf(
				"active state changed: agent=%p weights=%q blob=%q",
				runtime.active,
				runtime.activeWeights,
				runtime.activeBlob,
			)
		}
		if fake.loads != 1 || len(fake.systems) != 1 {
			t.Fatalf(
				"load calls = %d, init calls = %d, want 1, 1",
				fake.loads,
				len(fake.systems),
			)
		}
	})
}

func readCString(pointer *byte) string {
	if pointer == nil {
		return ""
	}
	data := make([]byte, 0, 64)
	for offset := uintptr(0); ; offset++ {
		value := *(*byte)(unsafe.Add(unsafe.Pointer(pointer), offset))
		if value == 0 {
			return string(data)
		}
		data = append(data, value)
	}
}
