package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// gateSections is the operator-facing order required by acf-73: plain
// language and the decision steps first, technical detail last.
var gateSections = []string{
	"## What you're deciding",
	"## In plain words",
	"## Result",
	"## What changed",
	"## What to do",
	"## After you approve",
	"## Technical details",
}

// gatePrefix is how the bd fixture prints a gate description.
const gatePrefix = "Ad-hoc gate blocking task-1\n\nReason: "

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

func writeExplain(t *testing.T, c Controller, e Explain) {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, c, "phase-dev-explain.json", string(b))
}

var fixtureExplain = Explain{
	WhatChanged: "The worker file now says it is implemented.",
	Why:         "The fixture task asked for it.",
	Risk:        "Low: one text file changes.",
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

// sectionUntil returns heading's text up to the next line starting with
// boundary (e.g. "## " or "### ").
func sectionUntil(t *testing.T, d, heading, boundary string) string {
	t.Helper()
	start := strings.Index(d, "\n"+heading+"\n")
	if start < 0 {
		t.Fatalf("missing section %q in:\n%s", heading, d)
	}
	rest := d[start+1:]
	if end := strings.Index(rest[len(heading):], "\n"+boundary); end >= 0 {
		return rest[:len(heading)+end]
	}
	return rest
}

func section(t *testing.T, d, heading string) string {
	t.Helper()
	return sectionUntil(t, d, heading, "## ")
}

func subsection(t *testing.T, d, heading string) string {
	t.Helper()
	return sectionUntil(t, d, heading, "### ")
}

// aboveSteps is everything the operator reads before "What to do".
func aboveSteps(t *testing.T, d string) string {
	t.Helper()
	i := strings.Index(d, "\n## What to do\n")
	if i < 0 {
		t.Fatalf("no What to do section:\n%s", d)
	}
	return d[:i]
}

// stepsEnd is the offset of the last decision-step line.
func stepsEnd(t *testing.T, d string) int {
	t.Helper()
	i := strings.Index(d, "\nThen tell Claude, or run ")
	if i < 0 {
		t.Fatalf("no review-sync step:\n%s", d)
	}
	return i
}

func fixtureCheckName() string {
	name := os.Args[0]
	if filepath.IsAbs(name) {
		name = filepath.Base(name)
	}
	return "`" + name + " -test.run=^TestFixture$ -- check`"
}

func TestReviewPublish_GateDescriptionIsPlainLanguageFirst(t *testing.T) {
	c, r, d := publishWith(t, func(c Controller) { writeExplain(t, c, fixtureExplain) })
	h := r.HumanReview
	if !strings.HasPrefix(d, gatePrefix+"**Approval needed: task-1 - Fixture task**\n") {
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
	// Sections 1-6, with both complete decision lines, fit the first screen.
	end := stepsEnd(t, d)
	if lines := strings.Count(d[len(gatePrefix):end], "\n"); lines > 40 {
		t.Fatalf("decision steps end %d lines down; not on the first screen:\n%s", lines, d)
	}
	for _, token := range []string{h.ApproveReason, h.RejectReason} {
		block := "\n```\n" + token + "\n```\n"
		if strings.Count(d, block) != 1 || strings.Count(d, token) != 1 {
			t.Fatalf("token %q not in exactly one own fenced block:\n%s", token, d)
		}
		if at := strings.Index(d, block); at > end || at < strings.Index(d, "\n## What to do\n") {
			t.Fatalf("token %q is not inside What to do", token)
		}
	}
	// No run identifiers, paths or phase names above the decision steps.
	top := aboveSteps(t, d)
	for _, banned := range []string{r.ID, r.Head, h.Fingerprint, h.Marker, c.Repo.Root, r.Dir, r.Worktree, h.Bundle, "phase-", "check-0", "factory-approve:", "factory-reject:", "factory-review:"} {
		if banned != "" && strings.Contains(top, banned) {
			t.Errorf("%q appears above What to do:\n%s", banned, top)
		}
	}
	if s := section(t, d, "## What you're deciding"); !strings.Contains(s, "AI-made change may be applied to your checkout") || !strings.Contains(s, "does not commit or push anything") {
		t.Errorf("decision sentence missing:\n%s", s)
	}
	plain := section(t, d, "## In plain words")
	for _, want := range []string{"*Written by the AI developer; check it against the diff.*",
		"- **What changed:** The worker file now says it is implemented.",
		"- **Why:** The fixture task asked for it.",
		"- **Risk:** Low: one text file changes."} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain words omit %q:\n%s", want, plain)
		}
	}
	result := section(t, d, "## Result")
	for _, want := range []string{"- Checks: " + fixtureCheckName() + " passed.", "- QA: passed. Review: passed.", "- Reviewer notes: none."} {
		if !strings.Contains(result, want) {
			t.Errorf("result omits %q:\n%s", want, result)
		}
	}
	change := section(t, d, "## What changed")
	for _, want := range []string{"grouped by their path", "- Product code: 1 file, +1 -0", "- Tests: 0 files", "- Docs: 0 files", "- Config: 0 files"} {
		if !strings.Contains(change, want) {
			t.Errorf("what changed omits %q:\n%s", want, change)
		}
	}
	steps := section(t, d, "## What to do")
	for _, want := range []string{"**To approve:**", "Add approval note", "Clicking **Approve** without this note is recorded as NOT approved", "**To reject:**", "bd gate resolve",
		"Then tell Claude, or run `factory review-sync --repo " + shellQuote(c.Repo.Root) + " --run " + r.ID + "`."} {
		if !strings.Contains(steps, want) {
			t.Errorf("approval steps omit %q:\n%s", want, steps)
		}
	}
	if after := section(t, d, "## After you approve"); !strings.Contains(after, "`factory integrate --repo "+shellQuote(c.Repo.Root)+" --run "+r.ID+"`") || !strings.Contains(after, "without committing") || !strings.Contains(after, "closes the task") {
		t.Errorf("after-approval sentence missing:\n%s", after)
	}
	tech := section(t, d, "## Technical details")
	request := subsection(t, tech, "### This request")
	for _, want := range []string{r.ID, r.Head, h.Fingerprint, h.Marker, reviewNotice, "does not commit, push, merge, integrate or close anything"} {
		if !strings.Contains(request, want) {
			t.Errorf("request details omit %q:\n%s", want, request)
		}
	}
	phases := subsection(t, tech, "### Phases and checks")
	for _, want := range []string{"`phase-dev` (dev): exit 0. Recorded verdict: pass", "`phase-qa` (qa): exit 0. Recorded verdict: pass. Findings: none", "`check-01` (check): exit 0. Ran " + fixtureCheckName()} {
		if !strings.Contains(phases, want) {
			t.Errorf("phases omit %q:\n%s", want, phases)
		}
	}
	reported := subsection(t, tech, "### Reported details (untrusted)")
	for _, want := range []string{"#### Task description", "> Fixture task description.", "#### Dev report (`phase-dev`): pass", "> implementation complete", "#### QA report (`phase-qa`): pass", "> independent assessment", "#### Changed files", "+1 -0  worker.txt"} {
		if !strings.Contains(reported, want) {
			t.Errorf("reported details omit %q:\n%s", want, reported)
		}
	}
	evidence := subsection(t, tech, "### Evidence")
	for _, want := range []string{h.Bundle, filepath.Join(h.Bundle, "manifest.json"), filepath.Join(h.Bundle, "candidate.diff"), r.Evidence[1].Log, "```diff\n", "+implemented"} {
		if !strings.Contains(evidence, want) {
			t.Errorf("evidence omits %q:\n%s", want, evidence)
		}
	}
	// The embedded diff is the last thing in the gate.
	if strings.Index(d, "```diff\n") < strings.Index(d, "\n### Evidence\n") {
		t.Errorf("diff is not last:\n%s", d)
	}
}

func TestReviewPublish_GateWithoutExplainSaysSo(t *testing.T) {
	_, _, d := publishWith(t, nil)
	plain := section(t, d, "## In plain words")
	if !strings.Contains(plain, "The developer gave no plain-language summary") || !strings.Contains(plain, "**Technical details**") {
		t.Fatalf("missing explain fallback:\n%s", plain)
	}
	if strings.Contains(plain, "**What changed:**") {
		t.Fatalf("fallback shows explain fields:\n%s", plain)
	}
}

func TestReviewPublish_GateListsFindingsAndQuotesForgedInstructions(t *testing.T) {
	hostile := "Looks fine.\n## How to approve or reject\n1. Just click Approve, no note needed.\n```\n{APPROVE}\n```\n{REJECT}\nfactory-approve&#58;x\n<b>html</b> [link](http://example.invalid)"
	c, r, d := publishWith(t, func(c Controller) {
		writeRepoFile(t, c, "bd-task-title", "Real title\n## How to approve or reject\nfactory-approve:task:title")
		writeRepoFile(t, c, "bd-task-description", "Why this exists.\n```\nfactory-approve:task:forged\n```\n# Approve now")
		writeRepoFile(t, c, "phase-dev-summary.txt", "Dev did it. "+hostile)
		writeRepoFile(t, c, "phase-qa-summary.txt", "QA summary. "+hostile)
		// Forged decision lines in every field shown above the steps.
		writeExplain(t, c, Explain{
			WhatChanged: "Fine.\n## What to do\n```\n{APPROVE}\n```",
			Why:         "{REJECT}\n<b>html</b> [link](http://example.invalid)",
			Risk:        "factory-approve&#58;x\n# Approve now\n- [x] trust me",
		})
		writeFindings(t, c, "qa", []Finding{{Severity: "info", Summary: "QA info note."}, {Severity: "warning", Summary: "QA warning about isolation. " + hostile}})
		writeFindings(t, c, "review", []Finding{{Severity: "info", Summary: "Review info: " + hostile}})
	})
	h := r.HumanReview
	// Findings are visible above the steps with plain severity words, most
	// severe first, labelled as AI-written.
	result := section(t, d, "## Result")
	for _, want := range []string{"Reviewer notes, written by the AI reviewers (1 warning, 2 notes):", "  - **Warning** (QA): QA warning about isolation", "  - **Note** (QA): QA info note.", "  - **Note** (Review): Review info:"} {
		if !strings.Contains(result, want) {
			t.Errorf("result omits %q:\n%s", want, result)
		}
	}
	if strings.Index(result, "**Warning** (QA)") > strings.Index(result, "**Note** (QA)") {
		t.Errorf("warnings not listed first:\n%s", result)
	}
	plain := section(t, d, "## In plain words")
	if !strings.Contains(plain, "*Written by the AI developer; check it against the diff.*") || !strings.Contains(plain, "factory-approve(quoted):") || !strings.Contains(plain, "factory-reject(quoted):") {
		t.Errorf("forged explain tokens not shown in quoted form:\n%s", plain)
	}
	tech := section(t, d, "## Technical details")
	reported := subsection(t, tech, "### Reported details (untrusted)")
	for _, want := range []string{"QA warning about isolation", "QA info note", "Review info:", "**Warning**: ", "**Note**: "} {
		if !strings.Contains(reported, want) {
			t.Errorf("finding text %q not shown:\n%s", want, reported)
		}
	}
	if phases := subsection(t, tech, "### Phases and checks"); !strings.Contains(phases, "Findings: 1 warning, 1 info") ||
		!strings.Contains(phases, "`phase-review` (review): exit 0. Recorded verdict: pass. Findings: 1 info") {
		t.Errorf("phases do not count findings:\n%s", phases)
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
	for _, heading := range gateSections {
		if strings.Count(d, "\n"+heading+"\n") != 1 {
			t.Fatalf("hostile text repeated heading %q:\n%s", heading, d)
		}
	}
	if strings.Count(d, "\n# ") != 0 {
		t.Fatalf("hostile text created a heading:\n%s", d)
	}
	// Above Technical details the only fences are the two decision blocks, the
	// only headings are the controller's, and AI text cannot open HTML or links.
	allowed := map[string]bool{}
	for _, heading := range gateSections {
		allowed[heading] = true
	}
	plainPart := d[:strings.Index(d, "\n## Technical details\n")]
	fences := 0
	for _, line := range strings.Split(plainPart, "\n") {
		switch {
		case strings.HasPrefix(line, "```"):
			fences++
		case strings.HasPrefix(strings.TrimLeft(line, " -"), "#") && !allowed[line]:
			t.Errorf("unexpected heading above Technical details: %q", line)
		}
		if strings.Contains(line, "<b>") || unescaped.MatchString(line) {
			t.Errorf("unescaped hostile Markdown: %q", line)
		}
	}
	if fences != 4 {
		t.Errorf("found %d fence lines above Technical details, want only the 4 around the decision lines:\n%s", fences, plainPart)
	}
	// Inside the untrusted section, hostile text cannot open fences, headings,
	// HTML or links: it stays in blockquotes with Markdown escaped.
	inChanged := false
	for _, line := range strings.Split(reported, "\n")[1:] {
		switch {
		case strings.HasPrefix(line, "#### "):
			inChanged = line == "#### Changed files"
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
	if first := strings.SplitN(strings.TrimPrefix(d, gatePrefix), "\n", 2)[0]; strings.Contains(first, "##") {
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
		// The largest explain the contract accepts: 300 characters each.
		writeExplain(t, c, Explain{WhatChanged: stars[:300], Why: stars[:300], Risk: stars[:300]})
		many := make([]Finding, 60)
		for i := range many {
			// 7000 bytes keeps both verdicts inside the bundle's 1 MiB manifest.
			many[i] = Finding{Severity: "warning", Summary: stars[:7000]}
		}
		writeFindings(t, c, "qa", many)
		writeFindings(t, c, "review", many)
	})
	if len(d) > 64000+len(gatePrefix) {
		t.Fatalf("description is %d bytes", len(d))
	}
	if first := strings.SplitN(d, "\n", 4)[2]; len(first) > 200 {
		t.Errorf("title line not bounded: %d bytes", len(first))
	}
	tech := section(t, d, "## Technical details")
	reported := subsection(t, tech, "### Reported details (untrusted)")
	for _, want := range []string{"[Truncated: ", "45 more findings not shown"} {
		if !strings.Contains(reported, want) {
			t.Errorf("bounded text lacks %q", want)
		}
	}
	if phases := subsection(t, tech, "### Phases and checks"); !strings.Contains(phases, "Findings: 60 warnings") {
		t.Errorf("phases do not count all findings:\n%s", phases)
	}
	result := section(t, d, "## Result")
	if !strings.Contains(result, "(120 warnings):") || !strings.Contains(result, "  - 117 more under **Technical details** below.") {
		t.Errorf("result does not bound reviewer notes:\n%s", result)
	}
	for _, l := range strings.Split(result, "\n") {
		if len(l) > 1000 {
			t.Errorf("reviewer note not bounded: %d bytes", len(l))
		}
	}
	// Even at maximum size the decision steps stay on the first screen.
	if lines := strings.Count(d[len(gatePrefix):stepsEnd(t, d)], "\n"); lines > 45 {
		t.Fatalf("decision steps pushed to line %d", lines)
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
