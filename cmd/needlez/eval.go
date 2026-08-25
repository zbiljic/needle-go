package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	needle "github.com/zbiljic/needle-go"
)

const (
	evalInputDisplayLimit         = 64
	evaluationFormatJSON          = "json"
	evaluationFormatText          = "text"
	evaluationReportSchemaVersion = 1
)

type evaluationCase struct {
	input string
	want  []evaluationExpectation
}

type evaluationExpectation struct {
	name         string
	arguments    json.RawMessage
	hasArguments bool
}

type evaluationResult struct {
	call       bool
	name       bool
	exact      bool
	actual     string
	detail     string
	confidence *float64
}

type evaluationReport struct {
	SchemaVersion int                    `json:"schema_version"`
	Cases         []evaluationCaseReport `json:"cases"`
	Summary       evaluationSummary      `json:"summary"`
}

type evaluationCaseReport struct {
	Index        int                           `json:"index"`
	Input        string                        `json:"input"`
	Want         []evaluationReportExpectation `json:"want"`
	Response     needle.Response               `json:"response"`
	CallResponse bool                          `json:"call_response"`
	NameMatch    bool                          `json:"name_match"`
	ExactMatch   bool                          `json:"exact_match"`
	DurationMS   int64                         `json:"duration_ms"`
	textResult   evaluationResult
}

type evaluationReportExpectation struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type evaluationSummary struct {
	Total             int      `json:"total"`
	CallResponses     int      `json:"call_responses"`
	NameMatches       int      `json:"name_matches"`
	ExactMatches      int      `json:"exact_matches"`
	Score             float64  `json:"score"`
	MinimumScore      float64  `json:"minimum_score"`
	Passed            bool     `json:"passed"`
	AverageConfidence *float64 `json:"average_confidence"`
	DurationMS        int64    `json:"duration_ms"`
}

func (a *application) runEval(ctx context.Context, args []string) error {
	flags := a.flagSet(
		`Evaluate a tool set against isolated JSONL prompt cases.

Each line must contain "input" and a "want" array. Expectations may be tool
names or objects with "name" and exact "arguments" fields.`,
		"eval [options]",
	)
	var options agentOptions
	addAgentFlags(flags, &options)
	casesPath := flags.String("cases", "", "path to JSONL evaluation cases")
	outputFormat := flags.String("format", evaluationFormatText, "output format: text or json")
	minScore := flags.Float64("min-score", 0, "minimum exact-match score required for success (0 disables)")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{errors.New("eval does not accept positional arguments")}
	}
	if options.toolsPath == "" {
		return usageError{errors.New("eval requires --tools")}
	}
	if *casesPath == "" {
		return usageError{errors.New("eval requires --cases")}
	}
	if *minScore < 0 || *minScore > 1 {
		return usageError{errors.New("--min-score must be between 0 and 1")}
	}
	if *outputFormat != evaluationFormatText && *outputFormat != evaluationFormatJSON {
		return usageError{errors.New(`--format must be "text" or "json"`)}
	}

	cases, err := readEvaluationCases(*casesPath)
	if err != nil {
		return fmt.Errorf("read cases: %w", err)
	}
	config, err := a.agentConfig(options)
	if err != nil {
		return err
	}
	if len(config.Tools) == 0 {
		return errors.New("tools file contains no schemas")
	}
	agent, err := a.deps.newAgent(ctx, config)
	if err != nil {
		return fmt.Errorf("initialize agent: %w", err)
	}

	started := time.Now()
	callCount, nameCount, exactCount := 0, 0, 0
	confidenceTotal, confidenceCount := 0.0, 0
	report := evaluationReport{
		SchemaVersion: evaluationReportSchemaVersion,
		Cases:         make([]evaluationCaseReport, 0, len(cases)),
	}
	if *outputFormat == evaluationFormatText {
		fmt.Fprintf(a.stdout, "Needle evaluation: %d cases\n", len(cases))
	}
	for index, evaluation := range cases {
		if err := agent.Reset(ctx); err != nil {
			return fmt.Errorf("case %d reset: %w", index+1, err)
		}
		caseStarted := time.Now()
		response, err := agent.Complete(ctx, evaluation.input, options.maxTokens)
		if err != nil {
			return fmt.Errorf("case %d complete: %w", index+1, err)
		}
		caseDuration := time.Since(caseStarted)
		result := evaluateResponse(response, evaluation.want)
		if result.call {
			callCount++
		}
		if result.name {
			nameCount++
		}
		if result.exact {
			exactCount++
		}
		if result.confidence != nil {
			confidenceTotal += *result.confidence
			confidenceCount++
		}
		caseReport := evaluationCaseReport{
			Index:        index + 1,
			Input:        evaluation.input,
			Want:         reportExpectations(evaluation.want),
			Response:     response,
			CallResponse: result.call,
			NameMatch:    result.name,
			ExactMatch:   result.exact,
			DurationMS:   caseDuration.Milliseconds(),
			textResult:   result,
		}
		report.Cases = append(report.Cases, caseReport)
		if *outputFormat == evaluationFormatText {
			writeTextEvaluationCase(a.stdout, caseReport, caseDuration)
		}
	}

	score := float64(exactCount) / float64(len(cases))
	passed := *minScore == 0 || score+1e-12 >= *minScore
	report.Summary = evaluationSummary{
		Total:         len(cases),
		CallResponses: callCount,
		NameMatches:   nameCount,
		ExactMatches:  exactCount,
		Score:         score,
		MinimumScore:  *minScore,
		Passed:        passed,
		DurationMS:    time.Since(started).Milliseconds(),
	}
	if confidenceCount != 0 {
		average := confidenceTotal / float64(confidenceCount)
		report.Summary.AverageConfidence = &average
	}
	if *outputFormat == evaluationFormatJSON {
		if err := writeJSON(a.stdout, report, false); err != nil {
			return err
		}
	} else {
		writeTextEvaluationSummary(a.stdout, report.Summary)
	}
	if !passed {
		return fmt.Errorf("exact-match score %.3f is below --min-score %.3f", score, *minScore)
	}
	return nil
}

func reportExpectations(want []evaluationExpectation) []evaluationReportExpectation {
	expectations := make([]evaluationReportExpectation, 0, len(want))
	for _, expectation := range want {
		reported := evaluationReportExpectation{Name: expectation.name}
		if expectation.hasArguments {
			reported.Arguments = append(json.RawMessage(nil), expectation.arguments...)
		}
		expectations = append(expectations, reported)
	}
	return expectations
}

func writeTextEvaluationCase(output io.Writer, report evaluationCaseReport, duration time.Duration) {
	confidence := "-"
	if report.textResult.confidence != nil {
		confidence = fmt.Sprintf("%.2f", *report.textResult.confidence)
	}
	status := "✗"
	if report.ExactMatch {
		status = "✓"
	}
	fmt.Fprintf(
		output,
		"%s %d %q -> %s (conf %s, %s)%s\n",
		status,
		report.Index,
		displayInput(report.Input),
		report.textResult.actual,
		confidence,
		duration.Round(time.Millisecond),
		report.textResult.detail,
	)
}

func writeTextEvaluationSummary(output io.Writer, summary evaluationSummary) {
	fmt.Fprintf(
		output,
		"calls %d/%d, names %d/%d, exact %d/%d, score %.3f",
		summary.CallResponses,
		summary.Total,
		summary.NameMatches,
		summary.Total,
		summary.ExactMatches,
		summary.Total,
		summary.Score,
	)
	if summary.AverageConfidence != nil {
		fmt.Fprintf(output, ", confidence %.2f", *summary.AverageConfidence)
	}
	fmt.Fprintf(output, ", %s\n", time.Duration(summary.DurationMS)*time.Millisecond)
}

func readEvaluationCases(path string) ([]evaluationCase, error) {
	data, err := readFileLimited(path, maxInputSize)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), maxInputSize)
	cases := make([]evaluationCase, 0)
	for line := 1; scanner.Scan(); line++ {
		data := bytes.TrimSpace(scanner.Bytes())
		if len(data) == 0 {
			continue
		}
		evaluation, err := parseEvaluationCase(data)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		cases = append(cases, evaluation)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(cases) == 0 {
		return nil, errors.New("no evaluation cases")
	}
	return cases, nil
}

func parseEvaluationCase(data []byte) (evaluationCase, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return evaluationCase{}, err
	}
	for field := range fields {
		if field != "input" && field != "want" {
			return evaluationCase{}, fmt.Errorf("unknown field %q", field)
		}
	}
	inputJSON, exists := fields["input"]
	if !exists {
		return evaluationCase{}, errors.New("input is required")
	}
	var input string
	if err := json.Unmarshal(inputJSON, &input); err != nil {
		return evaluationCase{}, fmt.Errorf("decode input: %w", err)
	}
	if strings.TrimSpace(input) == "" {
		return evaluationCase{}, errors.New("input is empty")
	}
	wantJSON, exists := fields["want"]
	if !exists {
		return evaluationCase{}, errors.New("want is required")
	}
	if bytes.Equal(bytes.TrimSpace(wantJSON), []byte("null")) {
		return evaluationCase{}, errors.New("want must be an array")
	}
	var rawExpectations []json.RawMessage
	if err := json.Unmarshal(wantJSON, &rawExpectations); err != nil {
		return evaluationCase{}, fmt.Errorf("decode want: %w", err)
	}
	want := make([]evaluationExpectation, 0, len(rawExpectations))
	for index, raw := range rawExpectations {
		expectation, err := parseEvaluationExpectation(raw)
		if err != nil {
			return evaluationCase{}, fmt.Errorf("want[%d]: %w", index, err)
		}
		want = append(want, expectation)
	}
	return evaluationCase{input: input, want: want}, nil
}

func parseEvaluationExpectation(data []byte) (evaluationExpectation, error) {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		if name == "" {
			return evaluationExpectation{}, errors.New("tool name is empty")
		}
		return evaluationExpectation{name: name}, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return evaluationExpectation{}, errors.New("expected a tool name or call object")
	}
	for field := range fields {
		if field != "name" && field != "arguments" {
			return evaluationExpectation{}, fmt.Errorf("unknown field %q", field)
		}
	}
	nameJSON, exists := fields["name"]
	if !exists {
		return evaluationExpectation{}, errors.New("name is required")
	}
	if err := json.Unmarshal(nameJSON, &name); err != nil {
		return evaluationExpectation{}, fmt.Errorf("decode name: %w", err)
	}
	if name == "" {
		return evaluationExpectation{}, errors.New("tool name is empty")
	}
	expectation := evaluationExpectation{name: name}
	if arguments, exists := fields["arguments"]; exists {
		expectation.arguments = append(json.RawMessage(nil), arguments...)
		expectation.hasArguments = true
	}
	return expectation, nil
}

func evaluateResponse(response needle.Response, want []evaluationExpectation) evaluationResult {
	actualNames := make([]string, 0, len(response.FunctionCalls))
	for _, call := range response.FunctionCalls {
		actualNames = append(actualNames, call.Name)
	}
	result := evaluationResult{
		call:       response.Success && response.Type == needle.ResponseCall,
		actual:     formatEvaluationCalls(response),
		confidence: response.Confidence,
	}
	if !response.Success {
		result.detail = " (response failed)"
		return result
	}
	if len(actualNames) != len(want) {
		result.detail = fmt.Sprintf(" (want %s)", formatExpectedCalls(want))
		return result
	}
	for index, expectation := range want {
		if actualNames[index] != expectation.name {
			result.detail = fmt.Sprintf(" (want %s)", formatExpectedCalls(want))
			return result
		}
	}
	result.name = true
	result.exact = true
	for index, expectation := range want {
		if expectation.hasArguments && !equalJSON(
			response.FunctionCalls[index].Arguments,
			expectation.arguments,
		) {
			result.exact = false
			result.detail = " (argument mismatch)"
			break
		}
	}
	return result
}

func equalJSON(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func formatEvaluationCalls(response needle.Response) string {
	if len(response.FunctionCalls) != 0 {
		names := make([]string, 0, len(response.FunctionCalls))
		for _, call := range response.FunctionCalls {
			names = append(names, call.Name)
		}
		return strings.Join(names, ">")
	}
	if response.Type == needle.ResponseRefuse {
		return "(refusal)"
	}
	if response.Type == needle.ResponseRespond {
		return "(respond)"
	}
	return "(no call)"
}

func formatExpectedCalls(want []evaluationExpectation) string {
	if len(want) == 0 {
		return "(no call)"
	}
	names := make([]string, 0, len(want))
	for _, expectation := range want {
		names = append(names, expectation.name)
	}
	return strings.Join(names, ">")
}

func displayInput(input string) string {
	input = strings.Join(strings.Fields(input), " ")
	if utf8.RuneCountInString(input) <= evalInputDisplayLimit {
		return input
	}
	runes := []rune(input)
	return string(runes[:evalInputDisplayLimit-1]) + "…"
}
