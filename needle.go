// Package needle provides Go access to the Needle tool-calling model.
package needle

import (
	"context"
	"encoding/json"
)

const (
	// DefaultGeneration is used when no generation or custom weights are selected.
	DefaultGeneration = 3
	// DefaultMaxSteps is the default maximum number of tool-calling rounds.
	DefaultMaxSteps = 8
	// DefaultMaxNewTokens is the default generation limit for each completion.
	DefaultMaxNewTokens = 512
	// DefaultBufferSize is the default native response buffer size.
	DefaultBufferSize = 64 * 1024
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

// Validation contains warnings produced by the Needle engine.
type Validation struct {
	Ungrounded []string `json:"ungrounded"`
	Negation   bool     `json:"negation"`
}

// Response is the structured response envelope returned by Needle.
type Response struct {
	Type          ResponseType   `json:"type"`
	Success       bool           `json:"success"`
	Error         *string        `json:"error"`
	ErrorCode     *string        `json:"error_code"`
	FunctionCalls []FunctionCall `json:"function_calls"`
	// SuppressedCalls are withheld by the engine and must not be executed automatically.
	SuppressedCalls []FunctionCall  `json:"suppressed_calls,omitempty"`
	Reasoning       string          `json:"reasoning"`
	Confidence      *float64        `json:"confidence"`
	PrefillTPS      float64         `json:"prefill_tps"`
	DecodeTPS       float64         `json:"decode_tps"`
	PeakRAMMB       float64         `json:"peak_ram_mb"`
	Results         []any           `json:"results,omitempty"`
	Validation      *Validation     `json:"validation,omitempty"`
	AudioText       *string         `json:"audio_text,omitempty"`
	AudioLanguage   *string         `json:"audio_language,omitempty"`
	AudioWords      []WordTimestamp `json:"audio_words,omitempty"`
	AudioTTFTMS     *float64        `json:"audio_ttft_ms,omitempty"`
	AudioDecodeTPS  *float64        `json:"audio_decode_tps,omitempty"`
}

// ToolSchema describes a function that the model may call.
type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
	// Triggers opt into native regex routing and can bypass the confidence floor.
	Triggers []string `json:"triggers,omitempty"`
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

// Config configures an Agent.
type Config struct {
	Tools  []Tool
	System string
	// Stateless resets the conversation before each Complete or Run request.
	// Run preserves context between tool rounds. The default is stateful; use
	// stateful mode for manual tool loops that continue through Complete.
	Stateless bool
	// Generation selects 2 or 3. Zero defaults to 3; WeightsPath takes precedence.
	Generation int
	// WeightsPath selects custom .cact weights and determines the generation.
	// Custom Needle 3 weights retain confidence when their archive carries a
	// recognized confidence head; other custom weights report nil. The header
	// and model-kind metadata are checked before loading. Remaining model bytes
	// and engine revision compatibility are validated by the native loader.
	WeightsPath   string
	ToolIndexPath string
	BufferSize    int

	// LibraryPath selects a trusted shared library for the selected generation.
	// When empty, New checks NEEDLE2_LIB_PATH or NEEDLE3_LIB_PATH, then fetches
	// the engine. NEEDLE_LIB_PATH is a legacy override for generation 2 only.
	LibraryPath string
	// CacheDir overrides the engine and base-weight download directory.
	CacheDir string
}

// Agent defines the Needle conversation lifecycle.
type Agent interface {
	// Complete performs raw inference without applying response validation.
	Complete(ctx context.Context, text string, maxNewTokens int) (Response, error)
	// Embed returns text features from Needle 3 without resetting the conversation.
	// Engines without embedding support return an error.
	Embed(ctx context.Context, text string) ([]float32, error)
	// Run uses ValidateResponse to reject an entire flagged turn before executing
	// any tool calls, without retrying. It returns the raw flagged response and
	// results from earlier completed rounds.
	Run(ctx context.Context, query string, maxSteps, maxNewTokens int) (Response, error)
	Reset(ctx context.Context) error
}
