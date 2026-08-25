package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	needle "github.com/zbiljic/needle-go"
)

type contactArguments struct {
	Name string `json:"name" jsonschema:"description=Full or partial contact name."`
}

func main() {
	ctx := context.Background()
	agent, err := needle.New(ctx, needle.Config{
		Tools: []needle.Tool{{Schema: needle.SchemaFor[contactArguments](
			"search_contacts",
			"Find a person in the user's contacts.",
		)}},
	})
	if err != nil {
		log.Fatal(err)
	}

	response, err := agent.Complete(ctx, "Find Ada Lovelace in my contacts.", needle.DefaultMaxNewTokens)
	if err != nil {
		log.Fatal(err)
	}
	if response.Type != needle.ResponseCall || len(response.FunctionCalls) == 0 {
		log.Fatalf("expected a tool call, got %s", response.Type)
	}

	call := response.FunctionCalls[0]
	var arguments contactArguments
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("call: %s(%q)\n", call.Name, arguments.Name)

	toolResults, err := json.Marshal([]any{map[string]any{
		"contact_id": "contact-123",
		"name":       arguments.Name,
	}})
	if err != nil {
		log.Fatal(err)
	}
	response, err = agent.Complete(ctx, string(toolResults), needle.DefaultMaxNewTokens)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("final response: %s\n", response.Type)
}
