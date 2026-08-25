package needle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

type agent struct {
	runtime       *processRuntime
	handlers      map[string]ToolHandler
	system        []byte
	tools         []byte
	weightsPath   string
	toolIndexPath []byte
	buffer        []byte
}

// New initializes an Agent. Unless Config.LibraryPath or NEEDLE_LIB_PATH names
// an existing shared library, New downloads and caches the engine for the
// current desktop platform.
func New(ctx context.Context, config Config) (Agent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prepared, err := prepareAgent(config)
	if err != nil {
		return nil, err
	}

	libraryPath, err := resolveLibraryPath(ctx, config)
	if err != nil {
		return nil, err
	}
	prepared.runtime = &defaultRuntime

	defaultRuntime.mu.Lock()
	defer defaultRuntime.mu.Unlock()
	if err := defaultRuntime.ensureLibraryLocked(libraryPath); err != nil {
		return nil, err
	}
	if err := defaultRuntime.bindLocked(prepared); err != nil {
		return nil, err
	}
	return prepared, nil
}

func prepareAgent(config Config) (*agent, error) {
	schemas := make([]ToolSchema, 0, len(config.Tools))
	handlers := make(map[string]ToolHandler, len(config.Tools))
	seen := make(map[string]struct{}, len(config.Tools))
	for _, tool := range config.Tools {
		if tool.Schema.Name == "" {
			return nil, errors.New("needle: tool name is empty")
		}
		if _, exists := seen[tool.Schema.Name]; exists {
			return nil, fmt.Errorf("needle: duplicate tool %q", tool.Schema.Name)
		}
		seen[tool.Schema.Name] = struct{}{}
		if tool.Schema.Parameters == nil {
			tool.Schema.Parameters = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}
		schemas = append(schemas, tool.Schema)
		if tool.Handler != nil {
			handlers[tool.Schema.Name] = tool.Handler
		}
	}

	toolsJSON, err := json.Marshal(schemas)
	if err != nil {
		return nil, fmt.Errorf("needle: encode tools: %w", err)
	}
	system, err := cString("system", config.System, false)
	if err != nil {
		return nil, err
	}
	tools, err := cString("tools", string(toolsJSON), false)
	if err != nil {
		return nil, err
	}
	toolIndexPath, err := cString("tool index path", config.ToolIndexPath, true)
	if err != nil {
		return nil, err
	}

	weightsPath := config.WeightsPath
	if weightsPath != "" {
		weightsPath, err = filepath.Abs(weightsPath)
		if err != nil {
			return nil, fmt.Errorf("needle: resolve weights path: %w", err)
		}
	}
	bufferSize := config.BufferSize
	if bufferSize == 0 {
		bufferSize = DefaultBufferSize
	}
	if bufferSize < 2 || bufferSize > math.MaxInt32 {
		return nil, fmt.Errorf("needle: invalid buffer size %d", bufferSize)
	}

	return &agent{
		handlers:      handlers,
		system:        system,
		tools:         tools,
		weightsPath:   weightsPath,
		toolIndexPath: toolIndexPath,
		buffer:        make([]byte, bufferSize),
	}, nil
}

func resolveLibraryPath(ctx context.Context, config Config) (string, error) {
	path := config.LibraryPath
	if path == "" {
		path = os.Getenv(EnvLibraryPath)
	}
	if path != "" {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("needle: resolve library path: %w", err)
		}
		return absolute, nil
	}
	return FetchEngine(ctx, FetchOptions{CacheDir: config.CacheDir})
}

func (a *agent) Complete(ctx context.Context, text string, maxNewTokens int) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if maxNewTokens == 0 {
		maxNewTokens = DefaultMaxNewTokens
	}
	if maxNewTokens < 1 || maxNewTokens > math.MaxInt32 {
		return Response{}, fmt.Errorf("needle: invalid max new tokens %d", maxNewTokens)
	}
	input, err := cString("input", text, false)
	if err != nil {
		return Response{}, err
	}

	a.runtime.mu.Lock()
	defer a.runtime.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if err := a.runtime.bindLocked(a); err != nil {
		return Response{}, err
	}
	clear(a.buffer)
	code := a.runtime.api.complete(
		bytePointer(input),
		int32(maxNewTokens),
		a.buffer,
		int32(len(a.buffer)),
	)
	if code < 0 {
		return Response{}, fmt.Errorf("needle: complete failed with code %d", code)
	}

	end := bytes.IndexByte(a.buffer, 0)
	if end < 0 {
		return Response{}, errors.New("needle: engine response exceeds buffer")
	}
	var response Response
	if err := json.Unmarshal(a.buffer[:end], &response); err != nil {
		return Response{}, fmt.Errorf("needle: decode response: %w", err)
	}
	if response.Type == "" {
		return Response{}, errors.New("needle: response type is empty")
	}
	if a.weightsPath != "" {
		response.Confidence = nil
	}
	return response, nil
}

func (a *agent) Run(ctx context.Context, query string, maxSteps, maxNewTokens int) (Response, error) {
	if maxSteps == 0 {
		maxSteps = DefaultMaxSteps
	}
	if maxSteps < 1 {
		return Response{}, fmt.Errorf("needle: invalid max steps %d", maxSteps)
	}

	response, err := a.Complete(ctx, query, maxNewTokens)
	if err != nil {
		return Response{}, err
	}
	executed := make([]any, 0)
	for range maxSteps {
		if response.Type != ResponseCall || len(response.FunctionCalls) == 0 {
			break
		}

		results := make([]any, 0, len(response.FunctionCalls))
		for _, call := range response.FunctionCalls {
			result := a.execute(ctx, call)
			results = append(results, result)
			executed = append(executed, result)
		}
		payload, err := json.Marshal(results)
		if err != nil {
			return Response{}, fmt.Errorf("needle: encode tool results: %w", err)
		}
		response, err = a.Complete(ctx, string(payload), maxNewTokens)
		if err != nil {
			return Response{}, err
		}
	}
	response.Results = executed
	return response, nil
}

func (a *agent) Reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.runtime.mu.Lock()
	defer a.runtime.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.runtime.bindLocked(a); err != nil {
		return err
	}
	a.runtime.api.reset()
	return nil
}

func (a *agent) execute(ctx context.Context, call FunctionCall) any {
	handler, ok := a.handlers[call.Name]
	if !ok {
		return map[string]string{"error": "unknown tool: " + call.Name}
	}
	arguments := call.Arguments
	if len(arguments) == 0 || bytes.Equal(arguments, []byte("null")) {
		arguments = json.RawMessage("{}")
	}
	result, err := handler(ctx, arguments)
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	return result
}

func cString(name, value string, nullable bool) ([]byte, error) {
	if strings.IndexByte(value, 0) >= 0 {
		return nil, fmt.Errorf("needle: %s contains NUL", name)
	}
	if nullable && value == "" {
		return nil, nil
	}
	return append([]byte(value), 0), nil
}

func bytePointer(value []byte) *byte {
	if len(value) == 0 {
		return nil
	}
	return &value[0]
}
