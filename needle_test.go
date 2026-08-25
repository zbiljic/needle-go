package needle

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestResponseJSON(t *testing.T) {
	t.Parallel()

	data := []byte(`{"type":"call","success":true,"error":null,"error_code":null,"function_calls":[{"name":"weather","arguments":{"city":"Lagos"}}],"reasoning":"city from query","confidence":0.94,"prefill_tps":4300,"decode_tps":850,"peak_ram_mb":28.5}`)
	var response Response
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if response.Type != ResponseCall || !response.Success {
		t.Fatalf("response = %#v", response)
	}
	if len(response.FunctionCalls) != 1 || response.FunctionCalls[0].Name != "weather" {
		t.Fatalf("function calls = %#v", response.FunctionCalls)
	}
	if got, want := string(response.FunctionCalls[0].Arguments), `{"city":"Lagos"}`; got != want {
		t.Fatalf("arguments = %s, want %s", got, want)
	}
	if response.Confidence == nil || *response.Confidence != 0.94 {
		t.Fatalf("confidence = %#v", response.Confidence)
	}
}

func TestToolSchemaJSON(t *testing.T) {
	t.Parallel()

	schema := ToolSchema{
		Name:        "weather",
		Description: "Get the weather for a city.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string"},
			},
			"required": []string{"city"},
		},
	}
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded ToolSchema
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded.Name != schema.Name || decoded.Description != schema.Description {
		t.Fatalf("decoded schema = %#v", decoded)
	}
}

func TestToolHandler(t *testing.T) {
	t.Parallel()

	handler := ToolHandler(func(_ context.Context, arguments json.RawMessage) (any, error) {
		var input map[string]string
		if err := json.Unmarshal(arguments, &input); err != nil {
			return nil, err
		}
		return input, nil
	})
	result, err := handler(context.Background(), json.RawMessage(`{"city":"Lagos"}`))
	if err != nil {
		t.Fatalf("handler() error = %v", err)
	}
	want := map[string]string{"city": "Lagos"}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("handler() = %#v, want %#v", result, want)
	}
}
