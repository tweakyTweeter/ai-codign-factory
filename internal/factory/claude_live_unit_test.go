package factory

import (
	"strings"
	"testing"
)

func TestCheckMarkerProvenance(t *testing.T) {
	const m = "MARK"
	init := `{"type":"system","subtype":"init","model":"haiku"}`
	initBad := `{"type":"system","subtype":"init","x":"MARK"}`
	text := `{"type":"assistant","message":{"content":[{"type":"text","text":"found MARK"}]}}`
	read := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/w/CLAUDE.md"}}]}}`
	readOther := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/w/a.go"}}]}}`
	bash := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"cat AGENTS.md"}}]}}`
	result := `{"type":"result","result":"done MARK"}`
	clean := `{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}`
	for _, tc := range []struct {
		name  string
		lines []string
		bad   bool
	}{
		{"no marker", []string{init, clean, `{"type":"result","result":"ok"}`}, false},
		{"marker after read", []string{init, read, text, result}, false},
		{"marker without read", []string{init, text}, true},
		{"marker before read", []string{init, text, read}, true},
		{"read of unrelated file", []string{init, readOther, text}, true},
		{"non-read tool", []string{init, bash, text}, true},
		{"marker in init", []string{initBad, read}, true},
		{"result only without read", []string{init, result}, true},
		{"malformed line", []string{init, "not json"}, true},
	} {
		err := checkMarkerProvenance([]byte(strings.Join(tc.lines, "\n")+"\n"), m)
		if (err != nil) != tc.bad {
			t.Errorf("%s: err=%v, want bad=%v", tc.name, err, tc.bad)
		}
	}
}
