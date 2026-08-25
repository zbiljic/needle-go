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
	if err := os.WriteFile(weightsPath, []byte("weights"), 0o600); err != nil {
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
	if string(fake.loaded) != "weights" || string(runtime.activeBlob) != "weights" {
		t.Fatalf("loaded weights = %q, retained = %q", fake.loaded, runtime.activeBlob)
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
