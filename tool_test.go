package needle

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type typedToolArguments struct {
	City  string `json:"city" jsonschema:"description=City whose weather should be returned."`
	Units string `json:"units,omitempty" jsonschema:"enum=celsius,enum=fahrenheit"`
}

type typedToolResult struct {
	Temperature int `json:"temperature"`
}

func TestNewTool(t *testing.T) {
	t.Parallel()

	type contextKey struct{}
	tool := NewTool("weather", "Get the weather.", func(ctx context.Context, arguments typedToolArguments) (typedToolResult, error) {
		if got := ctx.Value(contextKey{}); got != "request" {
			t.Fatalf("context value = %v, want request", got)
		}
		if arguments.City != "Lagos" {
			t.Fatalf("city = %q, want Lagos", arguments.City)
		}
		return typedToolResult{Temperature: 27}, nil
	})

	if tool.Schema.Name != "weather" || tool.Schema.Description != "Get the weather." {
		t.Fatalf("schema = %#v", tool.Schema)
	}
	result, err := tool.Handler(
		context.WithValue(context.Background(), contextKey{}, "request"),
		json.RawMessage(`{"city":"Lagos"}`),
	)
	if err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if want := (typedToolResult{Temperature: 27}); !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

func TestNewToolErrors(t *testing.T) {
	t.Parallel()

	wantHandlerError := errors.New("weather unavailable")
	tests := []struct {
		name      string
		raw       json.RawMessage
		handler   func(context.Context, typedToolArguments) (typedToolResult, error)
		wantError string
		wantIs    error
	}{
		{
			name: "invalid arguments",
			raw:  json.RawMessage(`{"city":`),
			handler: func(context.Context, typedToolArguments) (typedToolResult, error) {
				t.Fatal("handler called with invalid arguments")
				return typedToolResult{}, nil
			},
			wantError: `needle: decode arguments for tool "weather"`,
		},
		{
			name: "handler failure",
			raw:  json.RawMessage(`{"city":"Lagos"}`),
			handler: func(context.Context, typedToolArguments) (typedToolResult, error) {
				return typedToolResult{}, wantHandlerError
			},
			wantError: wantHandlerError.Error(),
			wantIs:    wantHandlerError,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tool := NewTool("weather", "", test.handler)
			_, err := tool.Handler(context.Background(), test.raw)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("handler error = %v, want containing %q", err, test.wantError)
			}
			if test.wantIs != nil && !errors.Is(err, test.wantIs) {
				t.Fatalf("handler error = %v, want errors.Is(%v)", err, test.wantIs)
			}
		})
	}
}

func TestNewToolWithoutHandler(t *testing.T) {
	t.Parallel()

	tool := NewTool[typedToolArguments, typedToolResult]("invoice", "Extract an invoice.", nil)
	if tool.Schema.Name != "invoice" || tool.Schema.Parameters == nil {
		t.Fatalf("schema = %#v", tool.Schema)
	}
	if tool.Handler != nil {
		t.Fatal("handler is not nil")
	}
}
