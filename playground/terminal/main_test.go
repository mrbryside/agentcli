package main

import (
	"os"
	"strings"
	"testing"
)

func TestPlaygroundDocumentsTaskManagedDelivery(t *testing.T) {
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read playground main: %v", err)
	}
	for _, required := range []string{
		"task tool returns foreground task output in the",
		"terminal never starts a result-continuation turn",
		"agent.RunTerminal",
	} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("playground main does not document %q", required)
		}
	}
}
