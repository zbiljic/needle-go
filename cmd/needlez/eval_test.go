package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	needle "github.com/zbiljic/needle-go"
)

func TestParseEvaluationCase(t *testing.T) {
	t.Parallel()

	evaluation, err := parseEvaluationCase([]byte(`{
		"input":"weather in Paris, then set a timer",
		"want":[
			"get_weather",
			{"name":"set_timer","arguments":{"minutes":10}}
		]
	}`))
	if err != nil {
		t.Fatalf("parseEvaluationCase() error = %v", err)
	}
	if evaluation.input != "weather in Paris, then set a timer" || len(evaluation.want) != 2 {
		t.Fatalf("evaluation = %#v", evaluation)
	}
	if evaluation.want[0].name != "get_weather" || evaluation.want[0].hasArguments {
		t.Fatalf("name-only expectation = %#v", evaluation.want[0])
	}
	if evaluation.want[1].name != "set_timer" || !evaluation.want[1].hasArguments ||
		!json.Valid(evaluation.want[1].arguments) {
		t.Fatalf("exact expectation = %#v", evaluation.want[1])
	}
}

func TestParseEvaluationCaseErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "missing input", data: `{"want":[]}`, want: "input is required"},
		{name: "empty input", data: `{"input":" ","want":[]}`, want: "input is empty"},
		{name: "missing want", data: `{"input":"hello"}`, want: "want is required"},
		{name: "null want", data: `{"input":"hello","want":null}`, want: "want must be an array"},
		{name: "unknown case field", data: `{"input":"hello","want":[],"extra":true}`, want: `unknown field "extra"`},
		{name: "empty tool name", data: `{"input":"hello","want":[""]}`, want: "tool name is empty"},
		{name: "missing call name", data: `{"input":"hello","want":[{"arguments":{}}]}`, want: "name is required"},
		{name: "unknown call field", data: `{"input":"hello","want":[{"name":"one","extra":true}]}`, want: `unknown field "extra"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseEvaluationCase([]byte(test.data))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseEvaluationCase() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestEvaluateResponse(t *testing.T) {
	t.Parallel()

	response := needle.Response{
		Type:    needle.ResponseCall,
		Success: true,
		FunctionCalls: []needle.FunctionCall{
			{Name: "get_weather", Arguments: json.RawMessage(`{"city":"Paris"}`)},
			{Name: "set_timer", Arguments: json.RawMessage(`{"minutes":10.0}`)},
		},
	}
	want := []evaluationExpectation{
		{name: "get_weather"},
		{name: "set_timer", arguments: json.RawMessage(`{"minutes":10}`), hasArguments: true},
	}
	result := evaluateResponse(response, want)
	if !result.call || !result.name || !result.exact || result.actual != "get_weather>set_timer" {
		t.Fatalf("evaluation result = %#v", result)
	}

	want[1].arguments = json.RawMessage(`{"minutes":5}`)
	result = evaluateResponse(response, want)
	if !result.name || result.exact || result.detail != " (argument mismatch)" {
		t.Fatalf("mismatched result = %#v", result)
	}

	result = evaluateResponse(needle.Response{Type: needle.ResponseRespond, Success: true}, nil)
	if result.call || !result.name || !result.exact || result.actual != "(respond)" {
		t.Fatalf("no-call result = %#v", result)
	}
}

func TestEval(t *testing.T) {
	t.Parallel()

	temporary := t.TempDir()
	toolsPath := filepath.Join(temporary, "tools.json")
	casesPath := filepath.Join(temporary, "cases.jsonl")
	writeTestFile(t, toolsPath, `[
		{"name":"get_weather","parameters":{"type":"object"}},
		{"name":"set_timer","parameters":{"type":"object"}}
	]`)
	writeTestFile(t, casesPath, strings.Join([]string{
		`{"input":"weather in Paris","want":["get_weather"]}`,
		`{"input":"timer for ten minutes","want":[{"name":"set_timer","arguments":{"minutes":10}}]}`,
	}, "\n"))

	confidence := 0.75
	fake := &fakeAgent{responses: []needle.Response{
		{
			Type:       needle.ResponseCall,
			Success:    true,
			Confidence: &confidence,
			FunctionCalls: []needle.FunctionCall{{
				Name:      "get_weather",
				Arguments: json.RawMessage(`{"city":"Paris"}`),
			}},
		},
		{
			Type:       needle.ResponseCall,
			Success:    true,
			Confidence: &confidence,
			FunctionCalls: []needle.FunctionCall{{
				Name:      "set_timer",
				Arguments: json.RawMessage(`{"minutes":10.0}`),
			}},
		},
	}}
	app, stdout, stderr := testApplication("")
	var gotConfig needle.Config
	app.deps.newAgent = func(_ context.Context, config needle.Config) (needle.Agent, error) {
		gotConfig = config
		return fake, nil
	}
	code := app.run(context.Background(), []string{
		"eval",
		"--tools", toolsPath,
		"--cases", casesPath,
		"--max-tokens", "64",
		"--min-score", "1",
	})
	if code != 0 {
		t.Fatalf("eval exit code = %d, stdout = %q stderr = %q", code, stdout.String(), stderr.String())
	}
	if fake.resets != 2 || !reflect.DeepEqual(fake.inputs, []string{"weather in Paris", "timer for ten minutes"}) {
		t.Fatalf("eval resets = %d inputs = %#v", fake.resets, fake.inputs)
	}
	if len(gotConfig.Tools) != 2 || gotConfig.Tools[1].Schema.Name != "set_timer" {
		t.Fatalf("eval tools = %#v", gotConfig.Tools)
	}
	if !reflect.DeepEqual(fake.tokens, []int{64, 64}) {
		t.Fatalf("eval tokens = %#v", fake.tokens)
	}
	if !strings.Contains(stdout.String(), "exact 2/2, score 1.000") ||
		!strings.Contains(stdout.String(), "confidence 0.75") {
		t.Fatalf("eval output = %q", stdout.String())
	}
}

func TestEvalMinimumScore(t *testing.T) {
	t.Parallel()

	temporary := t.TempDir()
	toolsPath := filepath.Join(temporary, "tools.json")
	casesPath := filepath.Join(temporary, "cases.jsonl")
	writeTestFile(t, toolsPath, `[{"name":"get_weather","parameters":{"type":"object"}}]`)
	writeTestFile(t, casesPath, `{"input":"weather in Paris","want":["get_weather"]}`)
	response := needle.Response{
		Type:    needle.ResponseCall,
		Success: true,
		FunctionCalls: []needle.FunctionCall{{
			Name:      "set_timer",
			Arguments: json.RawMessage(`{"minutes":10}`),
		}},
	}

	app, stdout, stderr := testApplication("")
	app.deps.newAgent = func(context.Context, needle.Config) (needle.Agent, error) {
		return &fakeAgent{responses: []needle.Response{response}}, nil
	}
	if code := app.run(context.Background(), []string{
		"eval", "--tools", toolsPath, "--cases", casesPath, "--min-score", "1",
	}); code != 1 {
		t.Fatalf("threshold eval exit code = %d", code)
	}
	if !strings.Contains(stdout.String(), "score 0.000") ||
		!strings.Contains(stderr.String(), "below --min-score") {
		t.Fatalf("threshold stdout = %q stderr = %q", stdout.String(), stderr.String())
	}

	app, _, stderr = testApplication("")
	app.deps.newAgent = func(context.Context, needle.Config) (needle.Agent, error) {
		return &fakeAgent{responses: []needle.Response{response}}, nil
	}
	if code := app.run(context.Background(), []string{
		"eval", "--tools", toolsPath, "--cases", casesPath,
	}); code != 0 {
		t.Fatalf("non-gating eval exit code = %d, stderr = %q", code, stderr.String())
	}
}

func TestEvalUsageAndInvalidCases(t *testing.T) {
	t.Parallel()

	app, _, _ := testApplication("")
	if code := app.run(context.Background(), []string{"eval"}); code != 2 {
		t.Fatalf("missing eval flags exit code = %d", code)
	}
	if code := app.run(context.Background(), []string{
		"eval", "--tools", "tools.json", "--cases", "cases.jsonl", "--min-score", "2",
	}); code != 2 {
		t.Fatalf("invalid score exit code = %d", code)
	}

	temporary := t.TempDir()
	toolsPath := filepath.Join(temporary, "tools.json")
	casesPath := filepath.Join(temporary, "cases.jsonl")
	writeTestFile(t, toolsPath, `[{"name":"one","parameters":{"type":"object"}}]`)
	writeTestFile(t, casesPath, "{}\n")
	app, _, stderr := testApplication("")
	if code := app.run(context.Background(), []string{
		"eval", "--tools", toolsPath, "--cases", casesPath,
	}); code != 1 {
		t.Fatalf("invalid cases exit code = %d", code)
	}
	if !strings.Contains(stderr.String(), "line 1: input is required") {
		t.Fatalf("invalid cases stderr = %q", stderr.String())
	}
}

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
