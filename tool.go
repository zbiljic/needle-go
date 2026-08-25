package needle

import (
	"context"
	"encoding/json"
	"fmt"
)

// NewTool builds a Tool whose parameter schema is derived from Arguments.
// Model-produced JSON arguments are decoded before the typed handler is called.
//
// A nil handler produces a schema-only tool suitable for use with Complete.
func NewTool[Arguments, Result any](
	name string,
	description string,
	handler func(context.Context, Arguments) (Result, error),
) Tool {
	tool := Tool{Schema: SchemaFor[Arguments](name, description)}
	if handler == nil {
		return tool
	}

	tool.Handler = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var arguments Arguments
		if err := json.Unmarshal(raw, &arguments); err != nil {
			return nil, fmt.Errorf("needle: decode arguments for tool %q: %w", name, err)
		}
		return handler(ctx, arguments)
	}
	return tool
}
