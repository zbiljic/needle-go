package needle

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestValidateResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		validation *Validation
		want       string
	}{
		{name: "absent"},
		{name: "empty", validation: &Validation{}},
		{name: "negation false", validation: &Validation{Negation: false}},
		{name: "negation first", validation: &Validation{Negation: true, Ungrounded: []string{"invoice.total"}}, want: "negation"},
		{name: "ungrounded supplied order", validation: &Validation{Ungrounded: []string{"invoice.total", "unknown.path"}}, want: "invoice.total, unknown.path"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := Response{Validation: test.validation, FunctionCalls: []FunctionCall{{Name: "invoice", Arguments: []byte(`{"total":1200}`)}}}
			beforeArguments := append(json.RawMessage(nil), response.FunctionCalls[0].Arguments...)
			var beforeUngrounded []string
			if test.validation != nil {
				beforeUngrounded = append([]string(nil), test.validation.Ungrounded...)
			}
			err := ValidateResponse(response)
			if (test.want == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateResponse() error = %v, want %q", err, test.want)
			}
			if !reflect.DeepEqual(response.FunctionCalls[0].Arguments, beforeArguments) || test.validation != nil && !reflect.DeepEqual(test.validation.Ungrounded, beforeUngrounded) {
				t.Fatalf("ValidateResponse() mutated response: %#v", response)
			}
		})
	}
}
