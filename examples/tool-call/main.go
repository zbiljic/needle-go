package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	needle "github.com/zbiljic/needle-go"
)

type weatherArguments struct {
	City string `json:"city" jsonschema:"description=City whose weather should be returned."`
}

type weatherResult struct {
	City        string `json:"city"`
	Temperature int    `json:"temperature_c"`
	Conditions  string `json:"conditions"`
}

func main() {
	ctx := context.Background()
	agent, err := needle.New(ctx, needle.Config{
		Tools: []needle.Tool{weatherTool()},
	})
	if err != nil {
		log.Fatal(err)
	}

	response, err := agent.Run(
		ctx,
		"What's the weather like in Lagos right now?",
		needle.DefaultMaxSteps,
		needle.DefaultMaxNewTokens,
	)
	if err != nil {
		log.Fatal(err)
	}

	results, err := json.MarshalIndent(response.Results, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("results: %s\n", results)
	if response.Confidence != nil {
		fmt.Printf("confidence: %.2f\n", *response.Confidence)
	}
}

func weatherTool() needle.Tool {
	return needle.NewTool(
		"get_weather",
		"Get the current weather for a city.",
		func(_ context.Context, arguments weatherArguments) (weatherResult, error) {
			if arguments.City == "" {
				return weatherResult{}, errors.New("city is required")
			}
			return weatherResult{
				City:        arguments.City,
				Temperature: 27,
				Conditions:  "clear",
			}, nil
		},
	)
}
