package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeContext(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "context.json")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunWritesOnlyRunAndTaskIDs(t *testing.T) {
	path := writeContext(t, `{"run":{"id":"run-123","task_id":"task-456"},"prompt":"do not emit"}`)
	var output bytes.Buffer

	if err := run(path, &output); err != nil {
		t.Fatal(err)
	}
	const want = "{\"run_id\":\"run-123\",\"task_id\":\"task-456\"}\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestRunRejectsInvalidInputs(t *testing.T) {
	oversized := strings.Repeat(" ", maxContextSize+1)
	tests := []struct {
		name        string
		path        string
		content     string
		wantErr     string
		missingFile bool
	}{
		{name: "missing environment variable", wantErr: "FACTORY_CONTEXT_FILE is not set"},
		{name: "unreadable file", path: filepath.Join(t.TempDir(), "missing.json"), wantErr: "cannot read context file", missingFile: true},
		{name: "malformed JSON", content: `{"run":`, wantErr: "invalid context JSON"},
		{name: "trailing JSON", content: `{"run":{"id":"run"}} {}`, wantErr: "invalid context JSON"},
		{name: "missing run ID", content: `{"run":{"task_id":"task"}}`, wantErr: "context is missing a run ID"},
		{name: "empty run ID", content: `{"run":{"id":"","task_id":"task"}}`, wantErr: "context is missing a run ID"},
		{name: "oversized input", content: oversized, wantErr: "context file exceeds 1 MiB"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := test.path
			if !test.missingFile && test.path != "" {
				t.Fatal("unexpected test path")
			}
			if test.content != "" {
				path = writeContext(t, test.content)
			}
			var output bytes.Buffer
			err := run(path, &output)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want error containing %q", err, test.wantErr)
			}
			if output.Len() != 0 {
				t.Fatalf("output = %q, want no output on error", output.String())
			}
		})
	}
}
