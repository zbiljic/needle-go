// Package needle defines Go interfaces and types for the Needle tool-calling model.
package needle

import (
	"context"
	"encoding/json"
)

const (
	// DefaultMaxSteps is the default maximum number of tool-calling rounds.
	DefaultMaxSteps = 8
	// DefaultMaxNewTokens is the default generation limit for each completion.
	DefaultMaxNewTokens = 256
)

// ResponseType identifies the action returned by Needle.
type ResponseType string

const (
	ResponseCall    ResponseType = "call"
	ResponseRespond ResponseType = "respond"
	ResponseRefuse  ResponseType = "refuse"
	ResponseText    ResponseType = "text"
)

// FunctionCall is a tool invocation selected by the model.
type FunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Response is the structured response envelope returned by Needle.
type Response struct {
	Type          ResponseType   `json:"type"`
	Success       bool           `json:"success"`
	Error         *string        `json:"error"`
	ErrorCode     *string        `json:"error_code"`
	FunctionCalls []FunctionCall `json:"function_calls"`
	Reasoning     string         `json:"reasoning"`
	Confidence    *float64       `json:"confidence"`
	PrefillTPS    float64        `json:"prefill_tps"`
	DecodeTPS     float64        `json:"decode_tps"`
	PeakRAMMB     float64        `json:"peak_ram_mb"`
	Results       []any          `json:"results,omitempty"`
}

// ToolSchema describes a function that the model may call.
type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolHandler executes a model-selected function call.
type ToolHandler func(context.Context, json.RawMessage) (any, error)

// Tool pairs a JSON schema with an optional Go implementation. A tool without
// a handler can be used with Complete, while Run reports it as unknown if the
// model selects it.
type Tool struct {
	Schema  ToolSchema
	Handler ToolHandler
}

// Agent defines the Needle conversation lifecycle.
type Agent interface {
	Complete(ctx context.Context, text string, maxNewTokens int) (Response, error)
	Run(ctx context.Context, query string, maxSteps, maxNewTokens int) (Response, error)
	Reset(ctx context.Context) error
}
