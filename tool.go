package needle

import (
	"context"
	"encoding/json"
	"fmt"
)

// NewTool builds a Tool from a schema and a typed Go handler. Arguments are
// decoded from the model's JSON object before the handler is called.
//
// A nil handler produces a schema-only tool suitable for use with Complete.
func NewTool[Arguments, Result any](
	schema ToolSchema,
	handler func(context.Context, Arguments) (Result, error),
) Tool {
	tool := Tool{Schema: schema}
	if handler == nil {
		return tool
	}

	tool.Handler = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var arguments Arguments
		if err := json.Unmarshal(raw, &arguments); err != nil {
			return nil, fmt.Errorf("needle: decode arguments for tool %q: %w", schema.Name, err)
		}
		return handler(ctx, arguments)
	}
	return tool
}
