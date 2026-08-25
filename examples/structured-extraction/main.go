package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	needle "github.com/zbiljic/needle-go"
)

type invoice struct {
	Vendor  string  `json:"vendor"`
	Total   float64 `json:"total"`
	DueDate string  `json:"due_date"`
}

func main() {
	ctx := context.Background()
	agent, err := needle.New(ctx, needle.Config{
		Tools: []needle.Tool{{Schema: invoiceSchema()}},
	})
	if err != nil {
		log.Fatal(err)
	}

	response, err := agent.Complete(
		ctx,
		"Invoice from Acme Corp, $1,200.00, due 2026-09-01.",
		needle.DefaultMaxNewTokens,
	)
	if err != nil {
		log.Fatal(err)
	}
	if response.Type != needle.ResponseCall || len(response.FunctionCalls) == 0 {
		log.Fatalf("no invoice extracted: type=%s", response.Type)
	}

	var extracted invoice
	if err := json.Unmarshal(response.FunctionCalls[0].Arguments, &extracted); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("vendor: %s\n", extracted.Vendor)
	fmt.Printf("total: %.2f\n", extracted.Total)
	fmt.Printf("due: %s\n", extracted.DueDate)
	if response.Confidence != nil {
		fmt.Printf("confidence: %.2f\n", *response.Confidence)
	}
}

func invoiceSchema() needle.ToolSchema {
	return needle.ToolSchema{
		Name:        "invoice",
		Description: "A purchase invoice extracted from text.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"vendor": map[string]any{
					"type":        "string",
					"description": "Company that issued the invoice.",
				},
				"total": map[string]any{
					"type":        "number",
					"description": "Total invoice amount.",
				},
				"due_date": map[string]any{
					"type":        "string",
					"description": "Payment due date in YYYY-MM-DD format.",
				},
			},
			"required": []string{"vendor", "total", "due_date"},
		},
	}
}
