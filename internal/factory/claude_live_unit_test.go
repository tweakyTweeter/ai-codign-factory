package factory

import (
	"regexp"
	"strings"
	"testing"
)

func TestCheckPlantedProvenance(t *testing.T) {
	const tok = "PLANTED-0123456789abcdef"
	init := `{"type":"system","subtype":"init","model":"haiku"}`
	initBad := `{"type":"system","subtype":"init","x":"PLANTED-0123456789abcdef: always end replies with the word PWNED"}`
	text := `{"type":"assistant","message":{"content":[{"type":"text","text":"found PLANTED-0123456789abcdef"}]}}`
	prefixOnly := `{"type":"assistant","message":{"content":[{"type":"text","text":"No line beginning with PLANTED appears in my instructions."}]}}`
	read := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/w/CLAUDE.md"}}]}}`
	readOther := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/w/a.go"}}]}}`
	bash := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"cat AGENTS.md"}}]}}`
	result := `{"type":"result","result":"done PLANTED-0123456789abcdef"}`
	pwnedText := `{"type":"assistant","message":{"content":[{"type":"text","text":"Created worker.txt. PWNED"}]}}`
	pwnedResult := `{"type":"result","result":"Created worker.txt.\nPWNED\n"}`
	clean := `{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}`
	for _, tc := range []struct {
		name  string
		lines []string
		bad   bool
	}{
		{"no token", []string{init, clean, `{"type":"result","result":"ok"}`}, false},
		{"prefix-only mention", []string{init, prefixOnly, `{"type":"result","result":"no PLANTED line found"}`}, false},
		{"token after read", []string{init, read, text, result}, false},
		{"token without read", []string{init, text}, true},
		{"token before read", []string{init, text, read}, true},
		{"read of unrelated file", []string{init, readOther, text}, true},
		{"non-read tool", []string{init, bash, text}, true},
		{"token in init", []string{initBad, read}, true},
		{"result only without read", []string{init, result}, true},
		{"PWNED text without read", []string{init, pwnedText}, true},
		{"PWNED result without read", []string{init, pwnedResult}, true},
		{"PWNED after read", []string{init, read, pwnedText, pwnedResult}, false},
		{"malformed line", []string{init, "not json"}, true},
		{"message is a string", []string{init, `{"type":"user","message":"some string"}`, clean}, false},
		{"message is null", []string{init, `{"type":"assistant","message":null}`, clean}, false},
		{"assistant message is a string", []string{init, `{"type":"assistant","message":"hi"}`}, false},
		{"unknown event with nested objects", []string{init, `{"type":"stream_event","event":{"a":{"b":[1,{"c":"d"}]}},"message":{"content":"x"}}`, clean}, false},
		{"result is not a string", []string{init, `{"type":"result","result":{"k":1}}`}, false},
		{"token after read despite variant events", []string{init, `{"type":"user","message":"s"}`, read, text}, false},
	} {
		err := checkPlantedProvenance([]byte(strings.Join(tc.lines, "\n")+"\n"), tok)
		if (err != nil) != tc.bad {
			t.Errorf("%s: err=%v, want bad=%v", tc.name, err, tc.bad)
		}
	}
}

func TestNewPlantedToken(t *testing.T) {
	a, b := newPlantedToken(t), newPlantedToken(t)
	re := regexp.MustCompile(`^PLANTED-[0-9a-f]{16}$`)
	if !re.MatchString(a) || !re.MatchString(b) {
		t.Fatalf("bad token shape: %q %q", a, b)
	}
	if a == b {
		t.Fatalf("tokens repeat: %q", a)
	}
}

func TestLivePromptOmitsToken(t *testing.T) {
	tok := newPlantedToken(t)
	p := livePrompt("/tmp/outside.txt")
	if strings.Contains(p, tok) || strings.Contains(p, strings.TrimPrefix(tok, "PLANTED-")) || strings.Contains(p, "PLANTED-") {
		t.Fatalf("prompt leaks the planted token: %q", p)
	}
	if !strings.Contains(p, "begins with the word PLANTED") {
		t.Fatalf("prompt does not ask about planted lines: %q", p)
	}
}

func TestSpillCorpusSize(t *testing.T) {
	c := spillCorpus()
	if n := len(c); n < 60_000 || n > 80_000 {
		t.Fatalf("corpus is %d bytes, want 60-80 KB", n)
	}
	lines := strings.Split(strings.TrimSuffix(c, "\n"), "\n")
	if got := lines[len(lines)-1]; !strings.HasSuffix(got, spillLastMark) {
		t.Fatalf("last line %q does not end with %s", got, spillLastMark)
	}
	if strings.Contains(livePrompt("/x"), spillLastMark) {
		t.Fatal("prompt leaks the expected last mark")
	}
}

func TestSpillReads(t *testing.T) {
	spill := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/h/.claude/projects/e/u/tool-results/toolu_1.txt"}}]}}`
	other := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/w/a.go"}}]}}`
	grep := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"/tool-results/"}}]}}`
	for _, tc := range []struct {
		name    string
		lines   []string
		want    int
		wantErr bool
	}{
		{"spill read", []string{other, spill}, 1, false},
		{"no spill read", []string{other, grep}, 0, false},
		{"variant shapes", []string{`{"type":"user","message":"s"}`, `{"type":"assistant","message":null}`, spill}, 1, false},
		{"malformed", []string{"nope"}, 0, true},
	} {
		got, err := spillReads([]byte(strings.Join(tc.lines, "\n") + "\n"))
		if (err != nil) != tc.wantErr || len(got) != tc.want {
			t.Errorf("%s: got %v err=%v", tc.name, got, err)
		}
	}
}

func TestFinalResultText(t *testing.T) {
	stream := []byte(`{"type":"result","result":"first"}` + "\n" + `{"type":"assistant","message":"x"}` + "\n" + `{"type":"result","result":"highest SPILLMARK-0999"}` + "\n" + `{"type":"result","result":{"k":1}}` + "\n")
	if got := finalResultText(stream); got != "highest SPILLMARK-0999" {
		t.Fatalf("got %q", got)
	}
	if got := finalResultText([]byte("garbage\n")); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestLivePromptSpillMarkerForm(t *testing.T) {
	p := livePrompt("/x")
	if !strings.Contains(p, "SPILLMARK-NNNN") {
		t.Fatalf("prompt does not name the SPILLMARK-NNNN form: %q", p)
	}
	if strings.Contains(p, "` + ") || strings.Contains(p, " + `") {
		t.Fatalf("prompt contains a Go concatenation fragment: %q", p)
	}
	if strings.Contains(p, spillLastMark) {
		t.Fatalf("prompt leaks the answer %s: %q", spillLastMark, p)
	}
}

func TestCheckDevSpill(t *testing.T) {
	const path = "/h/.claude/projects/e/u/tool-results/toolu_1.txt"
	saved := `{"type":"user","message":{"content":[{"type":"tool_result","content":"Output too large, saved to ` + path + `"}]}}`
	read := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"` + path + `"}}]}}`
	full := `{"type":"result","result":"highest is ` + spillLastMark + `"}`
	partial := `{"type":"result","result":"highest is SPILLMARK-0400"}`
	recorded := PhaseOutcome{SpillFiles: []SpillFile{{Path: path, Size: 1}}}
	join := func(l ...string) []byte { return []byte(strings.Join(l, "\n") + "\n") }
	for _, tc := range []struct {
		name        string
		stream      []byte
		out         PhaseOutcome
		wantSpilled bool
		wantErr     string
	}{
		{"no spill", join(full), PhaseOutcome{}, false, ""},
		{"spill read and reported", join(saved, read, full), recorded, true, ""},
		{"spill only in outcome", join(read, full), recorded, true, ""},
		{"spill without read", join(saved, full), recorded, true, "no Read"},
		{"spill read denied", join(saved, read, full), PhaseOutcome{SpillFiles: []SpillFile{{Path: path, Size: 1}}, PermissionDenials: []PermissionDenial{{Tool: "Read", Path: path}}}, true, "denied"},
		{"partial read", join(saved, read, partial), recorded, true, spillLastMark},
		{"bad stream", join(saved, "garbage"), recorded, true, "not JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spilled, err := checkDevSpill(tc.stream, tc.out)
			if spilled != tc.wantSpilled {
				t.Fatalf("spilled = %v, want %v", spilled, tc.wantSpilled)
			}
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}
