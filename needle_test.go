package needle

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
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

func TestResponseValidationJSON(t *testing.T) {
	t.Parallel()

	for _, input := range []string{`{"type":"respond"}`, `{"type":"respond","validation":null}`} {
		var response Response
		if err := json.Unmarshal([]byte(input), &response); err != nil || response.Validation != nil {
			t.Fatalf("json.Unmarshal(%s) = %#v, %v", input, response.Validation, err)
		}
	}
	emptyData, err := json.Marshal(Response{Type: ResponseRespond, Validation: &Validation{}})
	if err != nil || !strings.Contains(string(emptyData), `"validation":{"ungrounded":null,"negation":false}`) {
		t.Fatalf("empty validation JSON = %s, %v", emptyData, err)
	}
	absentData, err := json.Marshal(Response{Type: ResponseRespond})
	if err != nil || strings.Contains(string(absentData), `"validation"`) {
		t.Fatalf("absent validation JSON = %s, %v", absentData, err)
	}

	input := `{"type":"call","validation":{"ungrounded":["invoice.total","invoice.due_date"],"negation":true}}`
	var response Response
	if err := json.Unmarshal([]byte(input), &response); err != nil {
		t.Fatal(err)
	}
	if response.Validation == nil || !response.Validation.Negation || !reflect.DeepEqual(response.Validation.Ungrounded, []string{"invoice.total", "invoice.due_date"}) {
		t.Fatalf("validation = %#v", response.Validation)
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Response
	if err := json.Unmarshal(data, &roundTrip); err != nil || !reflect.DeepEqual(roundTrip.Validation, response.Validation) {
		t.Fatalf("round trip validation = %#v, %v", roundTrip.Validation, err)
	}

	for _, input := range []string{
		`{"type":"call","validation":{"ungrounded":true}}`,
		`{"type":"call","validation":{"negation":"yes"}}`,
	} {
		if err := json.Unmarshal([]byte(input), &Response{}); err == nil {
			t.Fatalf("json.Unmarshal(%s) error = nil", input)
		}
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
