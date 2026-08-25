package needle

import (
	"context"
	"os"
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
