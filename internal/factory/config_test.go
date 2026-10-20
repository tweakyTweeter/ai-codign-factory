package factory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeConfig_RejectsUnsafeOrUnknownConfiguration(t *testing.T) {
	valid := `{"version":1,"worker":{"argv":["worker"],"timeout":"1m"},"checks":[{"argv":["check"],"timeout":"1s"}]}`
	for name, input := range map[string]string{
		"unknown field":     strings.Replace(valid, `"version":1`, `"version":1,"deploy":true`, 1),
		"unknown version":   strings.Replace(valid, `"version":1`, `"version":2`, 1),
		"missing checks":    `{"version":1,"worker":{"argv":["worker"],"timeout":"1m"}}`,
		"negative timeout":  strings.Replace(valid, `"1m"`, `"-1m"`, 1),
		"unbounded timeout": strings.Replace(valid, `"1m"`, `"100h"`, 1),
		"zero timeout":      strings.Replace(valid, `"1m"`, `"0s"`, 1),
		"empty argv":        strings.Replace(valid, `["worker"]`, `[]`, 1),
		"NUL argument":      strings.Replace(valid, `["worker"]`, `["worker","\u0000"]`, 1),
		"trailing object":   valid + `{}`,
		"trailing junk":     valid + `garbage`,
		"null":              `null`,
		"empty beads":       strings.Replace(valid, `"version":1`, `"version":1,"beads_argv":[]`, 1),
		"oversized input":   valid + strings.Repeat(" ", 1<<20),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeConfig(strings.NewReader(input)); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	c, err := DecodeConfig(strings.NewReader(valid))
	if err != nil || c.BeadsArgv[0] != "bd" {
		t.Fatalf("valid config rejected: %+v: %v", c, err)
	}
	for i := 0; i < 33; i++ {
		c.Checks = append(c.Checks, c.Worker)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeConfig(strings.NewReader(string(b))); err == nil {
		t.Fatal("excessive checks accepted")
	}
}
