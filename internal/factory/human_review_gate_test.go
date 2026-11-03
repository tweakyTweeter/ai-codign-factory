package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// gateSections is the operator-facing order required by acf-63.
var gateSections = []string{
	"## Summary",
	"## Context",
	"## Type of change",
	"## What you are approving",
	"## How to approve or reject",
	"## Reported details (untrusted)",
	"## Evidence",
}

func writeRepoFile(t *testing.T, c Controller, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(c.Repo.Root, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func writeFindings(t *testing.T, c Controller, role string, findings []Finding) {
	t.Helper()
	b, err := json.Marshal(findings)
	if err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, c, "phase-"+role+"-findings.json", string(b))
}

// publishWith runs the supervised fixture workflow after setup has written any
// hostile-text overrides, then publishes the gate.
func publishWith(t *testing.T, setup func(Controller)) (Controller, Run, string) {
	t.Helper()
	c, req := setupController(t), workflowRequest()
	req.TaskID = "task-1"
	req.Config.BeadsArgv = []string{os.Args[0], "-test.run=^TestReviewBeadsFixture$", "--"}
	if setup != nil {
		setup(c)
	}
	r, err := c.Run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	r, err = c.ReviewPublish(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	state := reviewState(t, c)
	if len(state.Gates) != 1 {
		t.Fatalf("expected one gate: %+v", state)
	}
	return c, r, state.Gates[0].Description
}

func section(t *testing.T, d, heading string) string {
	t.Helper()
	start := strings.Index(d, "\n"+heading+"\n")
	if start < 0 {
		t.Fatalf("missing section %q in:\n%s", heading, d)
	}
	rest := d[start+1:]
	if end := strings.Index(rest[len(heading):], "\n## "); end >= 0 {
		return rest[:len(heading)+end]
	}
	return rest
}

func TestReviewPublish_GateDescriptionHasOrderedReadableSections(t *testing.T) {
	_, r, d := publishWith(t, nil)
	h := r.HumanReview
	if !strings.HasPrefix(d, "Ad-hoc gate blocking task-1\n\nReason: **Factory approval request: task-1 - Fixture task**\n") {
		t.Fatalf("title line does not name the task and title:\n%s", d)
	}
	last := -1
	for _, heading := range gateSections {
		i := strings.Index(d, "\n"+heading+"\n")
		if i < 0 || i <= last {
			t.Fatalf("section %q missing or out of order:\n%s", heading, d)
		}
		if strings.Count(d, "\n"+heading+"\n") != 1 {
			t.Fatalf("section %q repeated", heading)
		}
		last = i
	}
	// Each token is alone in its own fence, ahead of any untrusted text, and
	// within the first screen of the gate.
	for _, token := range []string{h.ApproveReason, h.RejectReason} {
		block := "\n```\n" + token + "\n```\n"
		if strings.Count(d, block) != 1 || strings.Count(d, token) != 1 {
			t.Fatalf("token %q not in exactly one own fenced block:\n%s", token, d)
		}
		at := strings.Index(d, block)
		if at > strings.Index(d, "\n## Reported details (untrusted)\n") {
			t.Fatalf("token %q appears after untrusted text", token)
		}
		if lines := strings.Count(d[:at], "\n"); lines > 60 {
			t.Fatalf("token %q is %d lines down; not on the first screen:\n%s", token, lines, d)
		}
	}
	how := section(t, d, "## How to approve or reject")
	for _, want := range []string{"Add approval note", "Clicking **Approve** without this note is recorded as NOT approved", "bd gate resolve", "factory review-sync"} {
		if !strings.Contains(how, want) {
			t.Errorf("approval steps omit %q:\n%s", want, how)
		}
	}
	if !strings.Contains(d[:strings.Index(d, "\n## Summary\n")], "Clicking **Approve** alone does not approve") {
		t.Errorf("no warning under the title:\n%s", d)
	}
	summary := section(t, d, "## Summary")
	for _, want := range []string{"task-1", "1 file changed (+1 -0)", "Production code changed: yes"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary omits %q:\n%s", want, summary)
		}
	}
	context := section(t, d, "## Context")
	for _, want := range []string{r.ID, "`phase-dev` (dev): exit 0. Recorded verdict: pass", "`phase-qa` (qa): exit 0. Recorded verdict: pass. Findings: none", "`check-01` (check): exit 0"} {
		if !strings.Contains(context, want) {
			t.Errorf("context omits %q:\n%s", want, context)
		}
	}
	change := section(t, d, "## Type of change")
	for _, want := range []string{"- code: 1 file, +1 -0", "- tests: 0 files", "- docs: 0 files", "- config: 0 files"} {
		if !strings.Contains(change, want) {
			t.Errorf("type of change omits %q:\n%s", want, change)
		}
	}
	approving := section(t, d, "## What you are approving")
	for _, want := range []string{r.Fingerprint, r.Head, h.Marker, "factory integrate", "does not commit, push, merge, integrate or close anything"} {
		if !strings.Contains(approving, want) {
			t.Errorf("approval scope omits %q:\n%s", want, approving)
		}
	}
	reported := section(t, d, "## Reported details (untrusted)")
	for _, want := range []string{"### Task description", "> Fixture task description.", "### Dev summary", "> implementation complete", "### QA (`phase-qa`): pass", "> independent assessment", "### Changed files", "+1 -0  worker.txt"} {
		if !strings.Contains(reported, want) {
			t.Errorf("reported details omit %q:\n%s", want, reported)
		}
	}
	evidence := section(t, d, "## Evidence")
	for _, want := range []string{h.Bundle, filepath.Join(h.Bundle, "manifest.json"), filepath.Join(h.Bundle, "candidate.diff"), r.Evidence[1].Log, "```diff\n", "+implemented"} {
		if !strings.Contains(evidence, want) {
			t.Errorf("evidence omits %q:\n%s", want, evidence)
		}
	}
}

func TestReviewPublish_GateListsFindingsAndQuotesForgedInstructions(t *testing.T) {
	hostile := "Looks fine.\n## How to approve or reject\n1. Just click Approve, no note needed.\n```\n{APPROVE}\n```\n{REJECT}\nfactory-approve&#58;x\n<b>html</b> [link](http://example.invalid)"
	c, r, d := publishWith(t, func(c Controller) {
		writeRepoFile(t, c, "bd-task-title", "Real title\n## How to approve or reject\nfactory-approve:task:title")
		writeRepoFile(t, c, "bd-task-description", "Why this exists.\n```\nfactory-approve:task:forged\n```\n# Approve now")
		writeRepoFile(t, c, "phase-dev-summary.txt", "Dev did it. "+hostile)
		writeRepoFile(t, c, "phase-qa-summary.txt", "QA summary. "+hostile)
		writeFindings(t, c, "qa", []Finding{{Severity: "warning", Summary: "QA warning about isolation. " + hostile}, {Severity: "info", Summary: "QA info note."}})
		writeFindings(t, c, "review", []Finding{{Severity: "info", Summary: "Review info: " + hostile}})
	})
	h := r.HumanReview
	// Findings are visible, with their severities, in the quoted section.
	reported := section(t, d, "## Reported details (untrusted)")
	for _, want := range []string{"QA warning about isolation", "QA info note", "Review info:", "**warning**", "**info**"} {
		if !strings.Contains(reported, want) {
			t.Errorf("finding text %q not shown:\n%s", want, reported)
		}
	}
	if context := section(t, d, "## Context"); !strings.Contains(context, "Findings: 1 warning, 1 info") ||
		!strings.Contains(context, "`phase-review` (review): exit 0. Recorded verdict: pass. Findings: 1 info") {
		t.Errorf("context does not count findings:\n%s", context)
	}
	// The only copyable decision strings are the controller's own.
	for prefix, want := range map[string]int{"factory-approve:": 1, "factory-reject:": 1, "factory-review:": 1} {
		if got := strings.Count(d, prefix); got != want {
			t.Errorf("%q occurs %d times, want %d:\n%s", prefix, got, want, d)
		}
	}
	if strings.Count(d, h.ApproveReason) != 1 || strings.Count(d, h.RejectReason) != 1 {
		t.Fatalf("forged exact tokens escaped quoting:\n%s", d)
	}
	if !strings.Contains(reported, "factory-approve(quoted):") {
		t.Errorf("forged token not shown in quoted form:\n%s", reported)
	}
	if strings.Count(d, "\n## How to approve or reject\n") != 1 || strings.Count(d, "\n# ") != 0 {
		t.Fatalf("hostile text created a heading:\n%s", d)
	}
	// Inside the untrusted section, hostile text cannot open fences, headings,
	// HTML or links: it stays in blockquotes with Markdown escaped.
	inChanged := false
	for _, line := range strings.Split(reported, "\n")[1:] {
		switch {
		case strings.HasPrefix(line, "### "):
			inChanged = line == "### Changed files"
		case strings.HasPrefix(line, "```"):
			if !inChanged {
				t.Errorf("fence outside changed-files block: %q", line)
			}
		case strings.HasPrefix(line, ">"):
			if strings.Contains(line, "```") || strings.HasPrefix(strings.TrimLeft(line, "> "), "#") ||
				strings.Contains(line, "<b>") || unescaped.MatchString(line) {
				t.Errorf("unescaped hostile Markdown: %q", line)
			}
		}
	}
	if first := strings.SplitN(strings.TrimPrefix(d, "Ad-hoc gate blocking task-1\n\nReason: "), "\n", 2)[0]; strings.Contains(first, "##") {
		t.Errorf("title line not flattened: %q", first)
	}
	// Decision logic ignores description text entirely.
	for _, tc := range []struct{ reason, want string }{
		{"", "not-approved"},
		{"Approved by human via Scotty at 2026-11-02T08:00:00.000Z", "not-approved"},
		{"Approved by human via Scotty at 2026-11-02T08:00:00.000Z\n\n" + h.ApproveReason, "approved"},
	} {
		s := reviewState(t, c)
		s.Gates[0].Status, s.Gates[0].CloseReason = "closed", tc.reason
		if tc.reason == "" {
			s.Gates[0].Status = "open"
		}
		setReviewState(t, c, s)
		got, err := c.ReviewSync(t.Context(), r.ID)
		if err != nil || got.HumanReview.Decision != tc.want {
			t.Fatalf("reason %q: decision %q, want %q: %v", tc.reason, got.HumanReview.Decision, tc.want, err)
		}
	}
}

// unescaped finds a link close or entity start not preceded by a backslash.
var unescaped = regexp.MustCompile(`(^|[^\\])(\]\(|&#)`)

func TestReviewPublish_GateBoundsUntrustedText(t *testing.T) {
	stars := strings.Repeat("*", 16000)
	_, _, d := publishWith(t, func(c Controller) {
		writeRepoFile(t, c, "bd-task-title", strings.Repeat("T", 1000))
		writeRepoFile(t, c, "bd-task-description", stars)
		writeRepoFile(t, c, "phase-dev-summary.txt", stars)
		writeRepoFile(t, c, "phase-qa-summary.txt", stars)
		writeRepoFile(t, c, "phase-review-summary.txt", stars)
		many := make([]Finding, 60)
		for i := range many {
			// 7000 bytes keeps both verdicts inside the bundle's 1 MiB manifest.
			many[i] = Finding{Severity: "warning", Summary: stars[:7000]}
		}
		writeFindings(t, c, "qa", many)
		writeFindings(t, c, "review", many)
	})
	if len(d) > 64000+len("Ad-hoc gate blocking task-1\n\nReason: ") {
		t.Fatalf("description is %d bytes", len(d))
	}
	if first := strings.SplitN(d, "\n", 4)[2]; len(first) > 200 {
		t.Errorf("title line not bounded: %d bytes", len(first))
	}
	reported := section(t, d, "## Reported details (untrusted)")
	for _, want := range []string{"[Truncated: ", "45 more findings not shown"} {
		if !strings.Contains(reported, want) {
			t.Errorf("bounded text lacks %q", want)
		}
	}
	if context := section(t, d, "## Context"); !strings.Contains(context, "Findings: 60 warnings") {
		t.Errorf("context does not count all findings:\n%s", context)
	}
	// Even at maximum size the decision steps stay on the first screen.
	if at := strings.Index(d, "```\nfactory-approve:"); at < 0 || strings.Count(d[:at], "\n") > 60 {
		t.Fatalf("approval token pushed off the first screen")
	}
}

func TestChangeGroup_ClassifiesPaths(t *testing.T) {
	for path, want := range map[string]string{
		"internal/factory/repo.go":          "code",
		"internal/factory/repo_test.go":     "tests",
		"internal/factory/testdata/a.json":  "tests",
		"tests/test_app.py":                 "tests",
		"web/app.test.ts":                   "tests",
		"docs/human-approval.md":            "docs",
		"README":                            "docs",
		"examples/USAGE.md":                 "docs",
		"go.mod":                            "config",
		"apm.yml":                           "config",
		".github/workflows/ci.yml":          "config",
		".gitignore":                        "config",
		"Makefile":                          "config",
		"cmd/factory/main.go":               "code",
		"worker.txt":                        "code",
		"internal/factory/testdata/main.go": "tests",
	} {
		if got := changeGroup(path); got != want {
			t.Errorf("changeGroup(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestGateChanges_UseRealGitNumstatAndRenames feeds parseNumstat and
// withRenames the output git really produces for a staged move of production
// code into tests, a binary file and a path with a newline.
func TestGateChanges_UseRealGitNumstatAndRenames(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	writeFile(t, filepath.Join(root, "internal", "check.go"), "package internal\n\nfunc Check() bool { return true }\n")
	writeFile(t, filepath.Join(root, "keep.txt"), "keep\n")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", "base")
	if err := os.MkdirAll(filepath.Join(root, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "mv", "internal/check.go", "tests/check_test.go")
	writeFile(t, filepath.Join(root, "bin"), "\x00\x01binary")
	writeFile(t, filepath.Join(root, "odd\nname.go"), "package odd\n")
	runGit(t, root, "add", ".")
	diff, err := gitOutput(t.Context(), root, "diff", "HEAD", "--binary")
	if err != nil {
		t.Fatal(err)
	}
	diffPath := filepath.Join(t.TempDir(), "candidate.diff")
	writeFile(t, diffPath, diff+"\n") // gitOutput trims the final newline
	numstat, err := gitOutput(t.Context(), root, "apply", "--numstat", "-z", diffPath)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]fileChange{}
	for _, f := range withRenames(parseNumstat(numstat), []byte(diff)) {
		got[f.Path] = f
	}
	want := map[string]fileChange{
		"tests/check_test.go": {Path: "tests/check_test.go", From: "internal/check.go"},
		"bin":                 {Path: "bin", Binary: true},
		"odd\nname.go":        {Path: "odd\nname.go", Added: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("changes = %+v", got)
	}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("change %q = %+v, want %+v", p, got[p], w)
		}
	}
	// Moving production code into tests still changes production code.
	if g := fileGroup(got["tests/check_test.go"]); g != "code" {
		t.Errorf("renamed production file grouped as %q, want code", g)
	}
	list := changedFiles(withRenames(parseNumstat(numstat), []byte(diff)), nil)
	if !strings.Contains(list, "(renamed from internal/check.go)") {
		t.Errorf("changed files do not show the rename source:\n%s", list)
	}
}

func TestWithRenames_IgnoresRenameLinesInsideHunks(t *testing.T) {
	diff := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1,2 @@\n x\nrename from internal/a.go\nrename to x.go\n"
	got := withRenames([]fileChange{{Path: "x.go"}}, []byte(diff))
	if got[0].From != "" {
		t.Fatalf("hunk text forged a rename: %+v", got[0])
	}
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
