package needle

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type extractionResult struct {
	Vendor string  `json:"vendor"`
	Total  float64 `json:"total"`
}

func TestExtract(t *testing.T) {
	t.Parallel()

	response := Response{
		Type: ResponseCall,
		FunctionCalls: []FunctionCall{{
			Name:      "invoice",
			Arguments: json.RawMessage(`{"vendor":"Acme","total":1200}`),
		}},
	}
	got, err := Extract[extractionResult](response)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	want := extractionResult{Vendor: "Acme", Total: 1200}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Extract() = %#v, want %#v", got, want)
	}
}

func TestExtractRejectsInvalidResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		response  Response
		wantError string
	}{
		{
			name:      "non-call response",
			response:  Response{Type: ResponseRespond},
			wantError: `response type "respond", want "call"`,
		},
		{
			name:      "no function calls",
			response:  Response{Type: ResponseCall},
			wantError: "got 0 function calls, want 1",
		},
		{
			name: "multiple function calls",
			response: Response{
				Type:          ResponseCall,
				FunctionCalls: []FunctionCall{{Name: "one"}, {Name: "two"}},
			},
			wantError: "got 2 function calls, want 1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Extract[extractionResult](test.response)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Extract() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestExtractReportsDecodeError(t *testing.T) {
	t.Parallel()

	response := Response{
		Type: ResponseCall,
		FunctionCalls: []FunctionCall{{
			Name:      "invoice",
			Arguments: json.RawMessage(`{"vendor":`),
		}},
	}
	_, err := Extract[extractionResult](response)
	if err == nil || !strings.Contains(err.Error(), `extract "invoice" arguments`) {
		t.Fatalf("Extract() error = %v", err)
	}
	var syntaxError *json.SyntaxError
	if !errors.As(err, &syntaxError) {
		t.Fatalf("Extract() error = %v, want json.SyntaxError", err)
	}
}
