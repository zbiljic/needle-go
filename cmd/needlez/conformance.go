package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	needle "github.com/zbiljic/needle-go"
)

type conformanceSuite struct {
	newAgent  func(context.Context, needle.Config) (needle.Agent, error)
	config    needle.Config
	maxTokens int
}

type conformanceCheck struct {
	name string
	run  func(context.Context) error
}

type conformanceWeatherArguments struct {
	City string `json:"city"`
	Day  string `json:"day,omitempty" jsonschema:"enum=today,enum=tomorrow,enum=weekend"`
}

type conformanceInvoice struct {
	Vendor  string  `json:"vendor"`
	Total   float64 `json:"total"`
	DueDate string  `json:"due_date"`
}

func (a *application) runTest(ctx context.Context, args []string) error {
	flags := a.flagSet("Run behavioral conformance checks against the native model.", "test [options]")
	libraryPath := flags.String("library", "", "path to libneedle shared library")
	cacheDir := flags.String("cache", "", "engine cache directory")
	bufferSize := flags.Int("buffer-size", 0, "native response buffer size")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{errors.New("test does not accept positional arguments")}
	}

	suite := conformanceSuite{
		newAgent: a.deps.newAgent,
		config: needle.Config{
			LibraryPath: *libraryPath,
			CacheDir:    *cacheDir,
			BufferSize:  *bufferSize,
		},
		maxTokens: needle.DefaultMaxNewTokens,
	}
	checks := suite.checks()
	failures := 0
	fmt.Fprintln(a.stdout, "Needle public API conformance")
	for _, check := range checks {
		if err := check.run(ctx); err != nil {
			failures++
			fmt.Fprintf(a.stdout, "✗ %s: %v\n", check.name, err)
			continue
		}
		fmt.Fprintf(a.stdout, "✓ %s\n", check.name)
	}
	if failures != 0 {
		fmt.Fprintf(a.stdout, "%d OF %d CHECKS FAILED\n", failures, len(checks))
		return fmt.Errorf("conformance checks failed")
	}
	fmt.Fprintf(a.stdout, "ALL %d CHECKS PASSED\n", len(checks))
	return nil
}

func (s conformanceSuite) checks() []conformanceCheck {
	return []conformanceCheck{
		{name: "no-tools completion", run: s.checkNoTools},
		{name: "tool selection", run: s.checkToolSelection},
		{name: "tool-result continuation", run: s.checkToolResult},
		{name: "conversation reset", run: s.checkReset},
		{name: "system facts", run: s.checkSystemFacts},
		{name: "structured extraction", run: s.checkExtraction},
	}
}

func (s conformanceSuite) checkNoTools(ctx context.Context) error {
	agent, err := s.agent(ctx, nil, "")
	if err != nil {
		return err
	}
	response, err := agent.Complete(ctx, "hello", s.maxTokens)
	if err != nil {
		return fmt.Errorf("complete: %w", err)
	}
	if err := validEnvelope(response); err != nil {
		return err
	}
	if response.Type != needle.ResponseCall || len(response.FunctionCalls) != 0 {
		return fmt.Errorf("got type %q with %d calls, want empty call", response.Type, len(response.FunctionCalls))
	}
	return nil
}

func (s conformanceSuite) checkToolSelection(ctx context.Context) error {
	agent, err := s.agent(ctx, []needle.Tool{conformanceWeatherTool()}, "")
	if err != nil {
		return err
	}
	response, err := agent.Complete(ctx, "what's the weather in Tokyo tomorrow", s.maxTokens)
	if err != nil {
		return fmt.Errorf("complete: %w", err)
	}
	arguments, err := weatherCall(response)
	if err != nil {
		return err
	}
	if !strings.EqualFold(arguments.City, "Tokyo") || arguments.Day != "tomorrow" {
		return fmt.Errorf("arguments = %#v, want Tokyo tomorrow", arguments)
	}
	return nil
}

func (s conformanceSuite) checkToolResult(ctx context.Context) error {
	agent, err := s.agent(ctx, []needle.Tool{conformanceWeatherTool()}, "")
	if err != nil {
		return err
	}
	response, err := agent.Complete(ctx, "what's the weather in Paris tomorrow", s.maxTokens)
	if err != nil {
		return fmt.Errorf("initial completion: %w", err)
	}
	if _, err := weatherCall(response); err != nil {
		return err
	}
	response, err = agent.Complete(
		ctx,
		`[{"city":"Paris","tomorrow":"rain, 14C"}]`,
		s.maxTokens,
	)
	if err != nil {
		return fmt.Errorf("result completion: %w", err)
	}
	if err := validEnvelope(response); err != nil {
		return err
	}
	if response.Type != needle.ResponseRespond {
		return fmt.Errorf("result response type = %q, want %q", response.Type, needle.ResponseRespond)
	}
	return nil
}

func (s conformanceSuite) checkReset(ctx context.Context) error {
	agent, err := s.agent(ctx, []needle.Tool{conformanceWeatherTool()}, "")
	if err != nil {
		return err
	}
	prompt := "what's the weather in Paris tomorrow"
	response, err := agent.Complete(ctx, prompt, s.maxTokens)
	if err != nil {
		return fmt.Errorf("initial completion: %w", err)
	}
	before, err := weatherCall(response)
	if err != nil {
		return err
	}
	if _, err := agent.Complete(ctx, `[{"city":"Paris","tomorrow":"rain, 14C"}]`, s.maxTokens); err != nil {
		return fmt.Errorf("result completion: %w", err)
	}
	if err := agent.Reset(ctx); err != nil {
		return fmt.Errorf("reset: %w", err)
	}
	response, err = agent.Complete(ctx, prompt, s.maxTokens)
	if err != nil {
		return fmt.Errorf("completion after reset: %w", err)
	}
	after, err := weatherCall(response)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(after, before) {
		return fmt.Errorf("call after reset = %#v, want %#v", after, before)
	}
	return nil
}

func (s conformanceSuite) checkSystemFacts(ctx context.Context) error {
	agent, err := s.agent(
		ctx,
		[]needle.Tool{conformanceWeatherTool()},
		"Today is 2026-07-30 and the user is in London.",
	)
	if err != nil {
		return err
	}
	response, err := agent.Complete(ctx, "what's the weather in London today", s.maxTokens)
	if err != nil {
		return fmt.Errorf("complete: %w", err)
	}
	_, err = singleCall(response, "get_weather")
	return err
}

func (s conformanceSuite) checkExtraction(ctx context.Context) error {
	schema := needle.SchemaFor[conformanceInvoice](
		"invoice",
		"A purchase invoice extracted from text.",
	)
	agent, err := s.agent(ctx, []needle.Tool{{Schema: schema}}, "")
	if err != nil {
		return err
	}
	response, err := agent.Complete(
		ctx,
		"Invoice from Acme Corp, $1,200.00, due 2026-09-01.",
		s.maxTokens,
	)
	if err != nil {
		return fmt.Errorf("complete: %w", err)
	}
	extracted, err := needle.Extract[conformanceInvoice](response)
	if err != nil {
		return err
	}
	if !strings.Contains(strings.ToLower(extracted.Vendor), "acme") ||
		math.Abs(extracted.Total-1200) > 0.001 ||
		extracted.DueDate != "2026-09-01" {
		return fmt.Errorf("extracted value = %#v", extracted)
	}
	return nil
}

func (s conformanceSuite) agent(
	ctx context.Context,
	tools []needle.Tool,
	system string,
) (needle.Agent, error) {
	config := s.config
	config.Tools = tools
	config.System = system
	agent, err := s.newAgent(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("initialize agent: %w", err)
	}
	return agent, nil
}

func conformanceWeatherTool() needle.Tool {
	return needle.Tool{Schema: needle.SchemaFor[conformanceWeatherArguments](
		"get_weather",
		"Current or forecast weather for a city.",
	)}
}

func weatherCall(response needle.Response) (conformanceWeatherArguments, error) {
	call, err := singleCall(response, "get_weather")
	if err != nil {
		return conformanceWeatherArguments{}, err
	}
	var arguments conformanceWeatherArguments
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return conformanceWeatherArguments{}, fmt.Errorf("decode weather arguments: %w", err)
	}
	return arguments, nil
}

func singleCall(response needle.Response, name string) (needle.FunctionCall, error) {
	if err := validEnvelope(response); err != nil {
		return needle.FunctionCall{}, err
	}
	if response.Type != needle.ResponseCall || len(response.FunctionCalls) != 1 {
		return needle.FunctionCall{}, fmt.Errorf(
			"got type %q with %d calls, want one %q call",
			response.Type,
			len(response.FunctionCalls),
			name,
		)
	}
	call := response.FunctionCalls[0]
	if call.Name != name {
		return needle.FunctionCall{}, fmt.Errorf("call name = %q, want %q", call.Name, name)
	}
	if !json.Valid(call.Arguments) {
		return needle.FunctionCall{}, errors.New("call arguments are not valid JSON")
	}
	return call, nil
}

func validEnvelope(response needle.Response) error {
	if !response.Success {
		if response.Error != nil {
			return fmt.Errorf("response failed: %s", *response.Error)
		}
		return errors.New("response failed")
	}
	if response.Type == "" {
		return errors.New("response type is empty")
	}
	if response.Confidence != nil && (*response.Confidence < 0 || *response.Confidence > 1) {
		return fmt.Errorf("confidence = %v, want [0,1]", *response.Confidence)
	}
	return nil
}
