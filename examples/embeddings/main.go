package main

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"math"
	"slices"

	needle "github.com/zbiljic/needle-go"
)

func main() {
	ctx := context.Background()
	agent, err := needle.New(ctx, needle.Config{})
	if err != nil {
		log.Fatal(err)
	}
	query, err := agent.Embed(ctx, "switch on the kitchen lights")
	if err != nil {
		log.Fatal(err)
	}
	type match struct {
		text  string
		score float64
	}
	var matches []match
	for _, text := range []string{"turn on the kitchen lights", "set an alarm for 6am", "get the weather in Paris"} {
		vector, err := agent.Embed(ctx, text)
		if err != nil {
			log.Fatal(err)
		}
		var dot, queryNorm, vectorNorm float64
		for i, value := range query {
			q, v := float64(value), float64(vector[i])
			dot += q * v
			queryNorm += q * q
			vectorNorm += v * v
		}
		score := 0.0
		if norm := math.Sqrt(queryNorm * vectorNorm); norm > 0 {
			score = dot / norm
		}
		matches = append(matches, match{text, score})
	}
	slices.SortFunc(matches, func(a, b match) int {
		return cmp.Compare(b.score, a.score)
	})
	for _, match := range matches {
		fmt.Printf("%.3f  %s\n", match.score, match.text)
	}
}
