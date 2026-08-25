package needle

import (
	"encoding/json"
	"fmt"
)

// Extract decodes a structured response containing exactly one function call
// into a value of type T.
func Extract[T any](response Response) (T, error) {
	var value T
	if response.Type != ResponseCall {
		return value, fmt.Errorf(
			"needle: extract: response type %q, want %q",
			response.Type,
			ResponseCall,
		)
	}
	if len(response.FunctionCalls) != 1 {
		return value, fmt.Errorf(
			"needle: extract: got %d function calls, want 1",
			len(response.FunctionCalls),
		)
	}

	call := response.FunctionCalls[0]
	if err := json.Unmarshal(call.Arguments, &value); err != nil {
		return value, fmt.Errorf("needle: extract %q arguments: %w", call.Name, err)
	}
	return value, nil
}
