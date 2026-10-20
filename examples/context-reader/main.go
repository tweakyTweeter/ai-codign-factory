package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const maxContextSize = 1 << 20

type context struct {
	Run struct {
		ID     string `json:"id"`
		TaskID string `json:"task_id"`
	} `json:"run"`
}

type output struct {
	RunID  string `json:"run_id"`
	TaskID string `json:"task_id"`
}

func run(contextFile string, writer io.Writer) error {
	if contextFile == "" {
		return errors.New("FACTORY_CONTEXT_FILE is not set")
	}

	file, err := os.Open(contextFile)
	if err != nil {
		return errors.New("cannot read context file")
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxContextSize+1))
	if err != nil {
		return errors.New("cannot read context file")
	}
	if len(data) > maxContextSize {
		return errors.New("context file exceeds 1 MiB")
	}

	var input context
	if err := json.Unmarshal(data, &input); err != nil {
		return errors.New("invalid context JSON")
	}
	if input.Run.ID == "" {
		return errors.New("context is missing a run ID")
	}

	if err := json.NewEncoder(writer).Encode(output{
		RunID:  input.Run.ID,
		TaskID: input.Run.TaskID,
	}); err != nil {
		return fmt.Errorf("cannot write output: %w", err)
	}
	return nil
}

func main() {
	if err := run(os.Getenv("FACTORY_CONTEXT_FILE"), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
