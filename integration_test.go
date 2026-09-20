package needle

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeEngine(t *testing.T) {
	libraryPath := os.Getenv("NEEDLE_TEST_LIBRARY")
	if libraryPath == "" {
		t.Skip("NEEDLE_TEST_LIBRARY is not set")
	}
	agent, err := New(context.Background(), Config{LibraryPath: libraryPath})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	response, err := agent.Complete(context.Background(), "hello", 32)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if response.Type == "" {
		t.Fatal("Complete() returned an empty response type")
	}
}

func TestNativeGenerations(t *testing.T) {
	if os.Getenv("NEEDLE_TEST_NATIVE") != "1" {
		t.Skip("set NEEDLE_TEST_NATIVE=1 to test both generations in one process")
	}
	ctx := context.Background()
	var agents []Agent
	for _, generation := range []int{2, 3} {
		a, err := New(ctx, Config{Generation: generation})
		if err != nil {
			t.Fatalf("generation %d: %v", generation, err)
		}
		agents = append(agents, a)
	}
	// Alternate generations after both libraries have been loaded.
	for range 2 {
		for i, a := range agents {
			if err := a.Reset(ctx); err != nil {
				t.Fatal(err)
			}
			r, err := a.Complete(ctx, "hello", DefaultMaxNewTokens)
			if err != nil || !r.Success || r.Type != ResponseCall || len(r.FunctionCalls) != 0 {
				t.Fatalf("generation %d: response=%+v err=%v", i+2, r, err)
			}
			if i == 1 && r.Confidence == nil {
				t.Fatal("v3 base model lost confidence")
			}
		}
	}
	// Reuse the published archive as a custom model to exercise real reloads
	// without needing a separately fine-tuned fixture.
	blob, err := os.ReadFile(runtimes[3].activeWeights)
	if err != nil {
		t.Fatal(err)
	}
	customPath := filepath.Join(t.TempDir(), "custom.cact")
	if err := os.WriteFile(customPath, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	custom, err := New(ctx, Config{Generation: 2, WeightsPath: customPath})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []Agent{custom, agents[1]} {
		r, err := a.Complete(ctx, "hello", DefaultMaxNewTokens)
		if err != nil || !r.Success || (r.Confidence == nil) != (a == custom) {
			t.Fatalf("custom=%v response=%+v err=%v", a == custom, r, err)
		}
	}
}
