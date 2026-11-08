package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Bounds for untrusted text quoted in a gate, in bytes before escaping.
// Escaping at most doubles them, so the description stays below the
// publication limit at least once the embedded diff is dropped.
const (
	gateTitleLimit       = 100
	gateTaskLimit        = 1500
	gateDevLimit         = 2500
	gateVerdictLimit     = 1200
	gateFindingLimit     = 400
	gateFindingsPerPhase = 15
	gateFilesLimit       = 50
	gatePathLimit        = 200
	gateDiffLimit        = 16000
	gateDescriptionLimit = 64000
	// Dev's plain-language fields hold at most 300 characters (1200 bytes).
	gateExplainLimit = 4 * explainFieldLimit
	// A configured check is named by its argv, shortened to this many bytes.
	gateCheckLimit = 120
	// Reviewer notes shown above the decision steps; the rest stay under
	// Technical details so the steps remain on the first screen.
	gateResultNotes = 3
)

// gateDescription builds the operator-facing gate text. It is persisted with
// the publish intent and later compared byte-for-byte with the native gate,
// so it is built exactly once per request. Plain language comes first (acf-73):
// the decision, Dev's plain summary, the result, the change counts and the
// decision steps; run identifiers, full reports and the diff come last under
// Technical details. AI- and author-written text is always escaped, bounded,
// defanged and labelled; only the controller's own decision lines are copyable.
// checks is the sealed run configuration's check list, used only to name each
// check by what it ran.
func (c Controller) gateDescription(ctx context.Context, r Run, h *HumanReview, checks []Command) (string, error) {
	diffPath := filepath.Join(h.Bundle, "candidate.diff")
	diff, err := readBounded(diffPath)
	if err != nil {
		return "", err
	}
	title, description := gateTask(r)
	var changes []fileChange
	// Statistics come from the bundled, digest-checked patch, not the worktree.
	numstat, statErr := gitOutput(ctx, r.Worktree, "apply", "--numstat", "-z", diffPath)
	if statErr == nil {
		changes = withRenames(parseNumstat(numstat), diff)
	}
	g := gateView{c: c, r: r, h: h, title: title, task: description, checks: checks, changes: changes, statErr: statErr}
	if full := g.render(diff, true); len(full) <= gateDescriptionLimit {
		return full, nil
	}
	if short := g.render(diff, false); len(short) <= gateDescriptionLimit {
		return short, nil
	}
	return "", errors.New("native inbox description exceeds 64000 bytes; refusing partial publication")
}

// gateTask reads the sealed task snapshot captured at claim time.
func gateTask(r Run) (title, description string) {
	b, err := readBounded(filepath.Join(r.Dir, "task.json"))
	if err != nil {
		return "", ""
	}
	var tasks []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if json.Unmarshal(b, &tasks) != nil || len(tasks) != 1 || tasks[0].ID != r.TaskID {
		return "", ""
	}
	return tasks[0].Title, tasks[0].Description
}

type gateView struct {
	c           Controller
	r           Run
	h           *HumanReview
	title, task string
	checks      []Command
	changes     []fileChange
	statErr     error
}

// render lays the gate out in the acf-73 order. Nothing above "What to do"
// names a run, HEAD, fingerprint, marker, path or phase; those, the full
// reports and the diff are under Technical details.
func (g gateView) render(diff []byte, embedDiff bool) string {
	r, h := g.r, g.h
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	root := shellQuote(g.c.Repo.Root)

	// 1. Title (task-author text: escaped and bounded).
	title := inlineUntrusted(g.title, gateTitleLimit)
	if title == "" {
		title = "(task title unavailable)"
	}
	line("**Approval needed: %s - %s**", r.TaskID, title)

	// 2. The decision.
	line("")
	line("## What you're deciding")
	line("Whether this AI-made change may be applied to your checkout. Approving does not commit or push anything.")

	// 3. Dev's plain-language summary: AI-written, one escaped line each.
	line("")
	line("## In plain words")
	if e := g.explain(); e != nil {
		line("*Written by the AI developer; check it against the diff.*")
		line("- **What changed:** %s", inlineUntrusted(e.WhatChanged, gateExplainLimit))
		line("- **Why:** %s", inlineUntrusted(e.Why, gateExplainLimit))
		line("- **Risk:** %s", inlineUntrusted(e.Risk, gateExplainLimit))
	} else {
		line("The developer gave no plain-language summary. Read the task and the developer's report under **Technical details** below.")
	}

	// 4. Result: checks by what they ran, reviews in words, reviewer notes.
	line("")
	line("## Result")
	var checks, reviews []string
	for _, e := range r.Evidence {
		switch {
		case e.Role == "check":
			checks = append(checks, g.checkName(e.Stage)+" "+outcomeWord(e))
		case (e.Role == "qa" || e.Role == "review") && e.Verdict != nil:
			reviews = append(reviews, roleName(e.Role)+": "+outcomeWord(e))
		}
	}
	if len(checks) == 0 {
		line("- Checks: none configured.")
	} else {
		line("- Checks: %s.", strings.Join(checks, "; "))
	}
	if len(reviews) == 0 {
		line("- AI reviews: none recorded.")
	} else {
		line("- %s.", strings.Join(reviews, ". "))
	}
	g.resultNotes(line)

	// 5. Change counts.
	line("")
	line("## What changed")
	if g.statErr != nil {
		line("File counts are unavailable; see **Technical details** below.")
	} else {
		totals := groupTotals(g.changes)
		line("Files, grouped by their path:")
		for _, group := range changeGroups {
			t := totals[group]
			if t.files == 0 {
				line("- %s: 0 files", groupLabels[group])
				continue
			}
			line("- %s: %s, +%d -%d", groupLabels[group], plural(t.files, "file"), t.added, t.deleted)
		}
	}

	// 6. The decision steps: the only copyable decision lines in the gate.
	line("")
	line("## What to do")
	line("**To approve:** click **Approve**, choose **Add approval note**, and paste this line as the note, on its own line:")
	line("")
	line("```")
	line("%s", h.ApproveReason)
	line("```")
	line("")
	line("Clicking **Approve** without this note is recorded as NOT approved.")
	line("")
	line("**To reject:** do not click Approve. Close this gate with exactly this reason, for example with `bd gate resolve <this gate's ID> --reason '<the line below>' --sandbox`:")
	line("")
	line("```")
	line("%s", h.RejectReason)
	line("```")
	line("")
	line("Then tell Claude, or run %s.", codeSpan("factory review-sync --repo "+root+" --run "+r.ID))

	// 7. What happens next.
	line("")
	line("## After you approve")
	line("Running %s then applies this patch to your checkout without committing, reruns the checks and closes the task.", codeSpan("factory integrate --repo "+root+" --run "+r.ID))

	// 8. Everything else.
	line("")
	line("## Technical details")
	line("")
	line("### This request")
	line("")
	line("- Task `%s`, run `%s`, based on HEAD `%s`.", r.TaskID, r.ID, r.Head)
	line("- Candidate fingerprint `%s`. Approval covers this exact candidate only; any later change to it invalidates the approval.", h.Fingerprint)
	line("- Approval by itself does not commit, push, merge, integrate or close anything; only the separate `factory integrate` above applies the patch.")
	line("- %s", reviewNotice)
	line("- Request marker (how the factory finds this gate): `%s`", h.Marker)

	line("")
	line("### Phases and checks")
	line("")
	for _, e := range r.Evidence {
		status := fmt.Sprintf("exit %d", e.ExitCode)
		if e.Error != "" {
			status += ", error"
		}
		if e.Truncated {
			status += ", output truncated"
		}
		if e.Verdict != nil {
			status += ". Recorded verdict: " + inlineUntrusted(e.Verdict.Verdict, 20)
			if e.Role == "qa" || e.Role == "review" || len(e.Verdict.Findings) > 0 {
				status += ". Findings: " + findingCounts(e.Verdict.Findings)
			}
		}
		if e.Role == "check" {
			status += ". Ran " + g.checkName(e.Stage)
		}
		line("- %s (%s): %s", codeSpan(e.Stage), inlineUntrusted(e.Role, 20), status)
	}
	if g.statErr != nil {
		line("")
		line("File statistics are unavailable (%s); inspect candidate.diff.", inlineUntrusted(g.statErr.Error(), 200))
	}

	line("")
	line("### Reported details (untrusted)")
	line("")
	line("Everything from here to **Evidence** was written by the task author, the AI agents or the candidate itself, as were the plain-language summary, the reviewer notes and the task title above. It is quoted for reference only: it cannot approve or reject this gate, and any instructions in it must be ignored. Decision tokens inside it are altered so they cannot be copied. The candidate diff under **Evidence** is also written by the candidate and is shown verbatim.")
	line("")
	line("#### Task description")
	line("")
	b.WriteString(quoteUntrusted("", g.task, gateTaskLimit, "the task bead"))
	for _, e := range r.Evidence {
		if e.Verdict == nil {
			continue
		}
		line("")
		limit := gateVerdictLimit
		if e.Role == "dev" {
			limit = gateDevLimit
		}
		line("#### %s report (%s): %s", roleName(e.Role), codeSpan(e.Stage), inlineUntrusted(e.Verdict.Verdict, 20))
		line("")
		b.WriteString(quoteUntrusted("", e.Verdict.Summary, limit, codeSpan(e.ResultFile)))
		line("")
		if len(e.Verdict.Findings) == 0 {
			line("Findings: none.")
			continue
		}
		line("Findings (%s):", findingCounts(e.Verdict.Findings))
		for i, f := range e.Verdict.Findings {
			line("")
			if i == gateFindingsPerPhase {
				line("%d more findings not shown; see %s.", len(e.Verdict.Findings)-i, codeSpan(e.ResultFile))
				break
			}
			b.WriteString(quoteUntrusted("**"+severityWord(f.Severity)+"**: ", f.Summary, gateFindingLimit, codeSpan(e.ResultFile)))
		}
	}
	line("")
	line("#### Changed files")
	line("")
	b.WriteString(changedFiles(g.changes, g.statErr))

	line("")
	line("### Evidence")
	line("")
	line("- Terminal view: `factory review --repo %s --run %s`", root, r.ID)
	line("- Bundle: %s", codeSpan(h.Bundle))
	line("- Manifest: %s", codeSpan(filepath.Join(h.Bundle, "manifest.json")))
	line("- Candidate diff: %s", codeSpan(filepath.Join(h.Bundle, "candidate.diff")))
	line("- Index diff (staged changes): %s", codeSpan(filepath.Join(h.Bundle, "index.diff")))
	line("- Phase logs and results:")
	for _, e := range r.Evidence {
		entry := fmt.Sprintf("  - %s: log %s", codeSpan(e.Stage), codeSpan(e.Log))
		if e.ResultFile != "" && e.Verdict != nil {
			entry += ", result " + codeSpan(e.ResultFile)
		}
		line("%s", entry)
	}
	line("")
	if embedDiff && len(diff) <= gateDiffLimit {
		b.WriteString("Candidate diff (complete):\n" + fencedDiff(string(diff)))
	} else {
		line("Diff too large for inbox embedding; no partial patch is represented as complete. Inspect the full local candidate.diff before deciding.")
	}
	return b.String()
}

var changeGroups = []string{"code", "tests", "docs", "config"}

type groupTotal struct{ files, added, deleted int }

func groupTotals(changes []fileChange) map[string]groupTotal {
	totals := map[string]groupTotal{}
	for _, f := range changes {
		group := fileGroup(f)
		t := totals[group]
		t.files++
		t.added += f.Added
		t.deleted += f.Deleted
		totals[group] = t
	}
	return totals
}

var groupLabels = map[string]string{"code": "Product code", "tests": "Tests", "docs": "Docs", "config": "Config"}

// explain returns Dev's plain-language fields, if the Dev verdict has them.
func (g gateView) explain() *Explain {
	var e *Explain
	for _, ev := range g.r.Evidence {
		if ev.Role == "dev" && ev.Verdict != nil {
			e = ev.Verdict.Explain
		}
	}
	return e
}

// checkName names a check stage by the argv it ran, from the run's sealed
// configuration. Absolute paths are shortened to their base name so no local
// path appears above the decision steps.
func (g gateView) checkName(stage string) string {
	n := 0
	if _, err := fmt.Sscanf(stage, "check-%d", &n); err != nil || n < 1 {
		return "A check"
	}
	if n > len(g.checks) || len(g.checks[n-1].Argv) == 0 {
		return fmt.Sprintf("Check %d", n)
	}
	args := make([]string, len(g.checks[n-1].Argv))
	for i, a := range g.checks[n-1].Argv {
		if filepath.IsAbs(a) {
			a = filepath.Base(a)
		}
		args[i] = a
	}
	s := strings.Join(args, " ")
	if len(s) > gateCheckLimit {
		s = truncateBytes(s, gateCheckLimit) + "..."
	}
	return codeSpan(s)
}

// outcomeWord states a phase or check result in words, never raw text.
func outcomeWord(e Evidence) string {
	if e.ExitCode == 0 && e.Error == "" && !e.Truncated && (e.Verdict == nil || e.Verdict.Verdict == "pass") {
		return "passed"
	}
	return "did not pass"
}

// resultNotes lists the first reviewer findings, most severe first, as
// escaped single lines labelled as AI-written; the rest stay under Technical
// details.
func (g gateView) resultNotes(line func(string, ...any)) {
	type note struct {
		role string
		f    Finding
	}
	var notes []note
	var all []Finding
	for _, e := range g.r.Evidence {
		if e.Verdict == nil || (e.Role != "qa" && e.Role != "review") {
			continue
		}
		for _, f := range e.Verdict.Findings {
			notes = append(notes, note{e.Role, f})
			all = append(all, f)
		}
	}
	if len(notes) == 0 {
		line("- Reviewer notes: none.")
		return
	}
	sort.SliceStable(notes, func(i, j int) bool { return severityRank(notes[i].f.Severity) < severityRank(notes[j].f.Severity) })
	line("- Reviewer notes, written by the AI reviewers (%s):", noteCounts(all))
	for _, n := range notes[:min(len(notes), gateResultNotes)] {
		line("  - **%s** (%s): %s", severityWord(n.f.Severity), roleName(n.role), inlineUntrusted(n.f.Summary, gateFindingLimit))
	}
	if len(notes) > gateResultNotes {
		line("  - %d more under **Technical details** below.", len(notes)-gateResultNotes)
	}
}

func severityRank(s string) int {
	switch s {
	case "blocking":
		return 0
	case "warning":
		return 1
	case "info":
		return 2
	}
	return 3
}

// severityWord maps the finding severity enum to a plain word; anything
// unexpected gets a generic word, never the raw text.
func severityWord(s string) string {
	switch s {
	case "blocking":
		return "Blocking"
	case "warning":
		return "Warning"
	case "info":
		return "Note"
	}
	return "Other"
}

// noteCounts counts findings with the plain severity words.
func noteCounts(findings []Finding) string {
	counts := map[string]int{}
	for _, f := range findings {
		counts[severityWord(f.Severity)]++
	}
	var parts []string
	if n := counts["Blocking"]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d blocking", n))
	}
	if n := counts["Warning"]; n > 0 {
		parts = append(parts, plural(n, "warning"))
	}
	if n := counts["Note"]; n > 0 {
		parts = append(parts, plural(n, "note"))
	}
	if n := counts["Other"]; n > 0 {
		parts = append(parts, plural(n, "other"))
	}
	return strings.Join(parts, ", ")
}

// changedFiles lists candidate-chosen paths in one inert fenced block.
func changedFiles(changes []fileChange, statErr error) string {
	if statErr != nil {
		return "Unavailable; inspect candidate.diff.\n"
	}
	if len(changes) == 0 {
		return "No file changes.\n"
	}
	totals := groupTotals(changes)
	var body strings.Builder
	shown := 0
	for _, group := range changeGroups {
		t := totals[group]
		if t.files == 0 {
			continue
		}
		fmt.Fprintf(&body, "%s (%s, +%d -%d)\n", group, plural(t.files, "file"), t.added, t.deleted)
		for _, f := range changes {
			if fileGroup(f) != group || shown >= gateFilesLimit {
				continue
			}
			shown++
			stat := fmt.Sprintf("+%d -%d", f.Added, f.Deleted)
			if f.Binary {
				stat = "binary"
			}
			entry := "  " + stat + "  " + displayPath(f.Path)
			if f.From != "" {
				entry += " (renamed from " + displayPath(f.From) + ")"
			}
			body.WriteString(entry + "\n")
		}
	}
	if shown < len(changes) {
		fmt.Fprintf(&body, "... %d more files; see candidate.diff\n", len(changes)-shown)
	}
	fence := "```"
	for strings.Contains(body.String(), fence) {
		fence += "`"
	}
	return fence + "text\n" + body.String() + fence + "\n"
}

// displayPath keeps a candidate-chosen path on one bounded, inert line.
func displayPath(p string) string {
	if !utf8.ValidString(p) || strings.IndexFunc(p, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		p = strconv.Quote(p)
	}
	if len(p) > gatePathLimit {
		p = truncateBytes(p, gatePathLimit) + "..."
	}
	return defangTokens(p)
}

type fileChange struct {
	Path, From     string
	Added, Deleted int
	Binary         bool
}

// parseNumstat reads `git apply --numstat -z`: "added\tdeleted\tpath\0",
// with "-" counts for binary files. For a rename git prints only the new
// path; withRenames recovers the old one from the patch headers.
func parseNumstat(raw string) []fileChange {
	var out []fileChange
	for _, field := range strings.Split(raw, "\x00") {
		parts := strings.SplitN(field, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		f := fileChange{Path: parts[2]}
		if parts[0] == "-" && parts[1] == "-" {
			f.Binary = true
		} else {
			f.Added, _ = strconv.Atoi(parts[0])
			f.Deleted, _ = strconv.Atoi(parts[1])
		}
		out = append(out, f)
	}
	return out
}

// withRenames sets From for every change whose new path a "rename from" /
// "rename to" header pair in the patch names. Only extended headers between
// "diff --git" and the first hunk or binary marker are read, so patch content
// cannot forge a rename.
func withRenames(changes []fileChange, diff []byte) []fileChange {
	renames := map[string]string{}
	inHeader := false
	from := ""
	for _, l := range strings.Split(string(diff), "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			inHeader, from = true, ""
		case !inHeader:
		case strings.HasPrefix(l, "rename from "):
			from = gitPath(strings.TrimPrefix(l, "rename from "))
		case strings.HasPrefix(l, "rename to "):
			if from != "" {
				renames[gitPath(strings.TrimPrefix(l, "rename to "))] = from
			}
		case strings.HasPrefix(l, "--- "), strings.HasPrefix(l, "+++ "), strings.HasPrefix(l, "@@"),
			strings.HasPrefix(l, "Binary files "), strings.HasPrefix(l, "GIT binary patch"):
			inHeader = false
		}
	}
	for i := range changes {
		if old, ok := renames[changes[i].Path]; ok {
			changes[i].From = old
		}
	}
	return changes
}

// gitPath undoes git's C-style quoting of a header path.
func gitPath(p string) string {
	if strings.HasPrefix(p, "\"") {
		if u, err := strconv.Unquote(p); err == nil {
			return u
		}
	}
	return p
}

// fileGroup classifies a change; a rename counts as code when either side is
// code, because moving production code out of a code path still changes it.
func fileGroup(f fileChange) string {
	if f.From != "" && changeGroup(f.From) == "code" {
		return "code"
	}
	return changeGroup(f.Path)
}

var testFileName = regexp.MustCompile(`(_test\.go|^test_.*\.py|_test\.py|\.(test|spec)\.[^.]+)$`)

// changeGroup is a path heuristic for the operator's overview only; no
// authorization decision depends on it.
func changeGroup(p string) string {
	base := path.Base(p)
	dirs := strings.Split(path.Dir(p), "/")
	for _, s := range dirs {
		switch s {
		case "test", "tests", "testdata", "__tests__", "spec":
			return "tests"
		}
	}
	if testFileName.MatchString(base) {
		return "tests"
	}
	ext := strings.ToLower(path.Ext(base))
	switch ext {
	case ".md", ".markdown", ".rst", ".adoc":
		return "docs"
	}
	if dirs[0] == "docs" || dirs[0] == "doc" {
		return "docs"
	}
	for _, prefix := range []string{"README", "LICENSE", "CHANGELOG", "CONTRIBUTING", "NOTICE", "AUTHORS"} {
		if strings.HasPrefix(strings.ToUpper(base), prefix) {
			return "docs"
		}
	}
	switch ext {
	case ".json", ".yaml", ".yml", ".toml", ".ini", ".cfg", ".conf", ".mod", ".sum", ".lock":
		return "config"
	}
	switch base {
	case "Makefile", "Dockerfile", "Containerfile", "Justfile":
		return "config"
	}
	if strings.HasPrefix(base, ".") {
		return "config"
	}
	for _, s := range dirs {
		if strings.HasPrefix(s, ".") && s != "." && s != ".." {
			return "config"
		}
	}
	return "code"
}

func roleName(role string) string {
	switch role {
	case "qa":
		return "QA"
	case "review":
		return "Review"
	case "dev":
		return "Dev"
	}
	return inlineUntrusted(role, 20)
}

func findingCounts(findings []Finding) string {
	if len(findings) == 0 {
		return "none"
	}
	counts := map[string]int{}
	for _, f := range findings {
		counts[f.Severity]++
	}
	var parts []string
	other := len(findings)
	for _, s := range []string{"blocking", "warning", "info"} {
		if counts[s] > 0 {
			parts = append(parts, plural(counts[s], s))
			other -= counts[s]
		}
	}
	if other > 0 {
		parts = append(parts, plural(other, "other"))
	}
	return strings.Join(parts, ", ")
}

func plural(n int, word string) string {
	switch {
	case n == 1:
		return "1 " + word
	case word == "info" || word == "other":
		return strconv.Itoa(n) + " " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// codeSpan renders controller-chosen text (paths, stage names) as inline code.
func codeSpan(s string) string {
	s = strings.ReplaceAll(cleanText(s), "\n", " ")
	fence := "`"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return fence + defangTokens(s) + fence
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("/._-+:@", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var tokenDefang = strings.NewReplacer(
	"factory-approve:", "factory-approve(quoted):",
	"factory-reject:", "factory-reject(quoted):",
	"factory-review:", "factory-review(quoted):",
)

// defangTokens makes decision-token-shaped text inert, so the only copyable
// approval and rejection tokens in a gate are the controller's own.
func defangTokens(s string) string { return tokenDefang.Replace(s) }

// cleanText keeps valid UTF-8 printable text and newlines, dropping control
// and format characters (including bidirectional overrides).
func cleanText(s string) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
}

func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

var orderedMarker = regexp.MustCompile(`^(\s*\d+)([.)])`)

// escapeMarkdown backslash-escapes every character that can open inline or
// block Markdown (code, emphasis, links, images, HTML, entities, headings,
// tables), plus list and setext markers at the start of a line.
func escapeMarkdown(line string) string {
	var b strings.Builder
	for _, r := range line {
		if strings.ContainsRune("\\`*_[]<>#|~&!", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	out := b.String()
	trimmed := strings.TrimLeft(out, " ")
	if trimmed != "" && strings.ContainsRune("-+=", rune(trimmed[0])) {
		out = out[:len(out)-len(trimmed)] + "\\" + trimmed
	}
	return orderedMarker.ReplaceAllString(out, `$1\$2`)
}

// inlineUntrusted flattens untrusted text to one bounded, escaped line.
func inlineUntrusted(s string, limit int) string {
	s = strings.Join(strings.Fields(cleanText(s)), " ")
	if len(s) > limit {
		s = truncateBytes(s, limit) + "..."
	}
	return escapeMarkdown(defangTokens(s))
}

// quoteUntrusted renders untrusted text as a bounded, escaped blockquote,
// optionally led by trusted (already safe) Markdown.
func quoteUntrusted(lead, s string, limit int, source string) string {
	s = strings.TrimSpace(cleanText(s))
	if s == "" && lead == "" {
		return "> (empty)\n"
	}
	cut := 0
	if len(s) > limit {
		kept := truncateBytes(s, limit)
		cut, s = len(s)-len(kept), kept
	}
	var b strings.Builder
	for i, l := range strings.Split(defangTokens(s), "\n") {
		l = escapeMarkdown(l)
		if i == 0 {
			l = lead + l
		}
		if strings.TrimSpace(l) == "" {
			b.WriteString(">\n")
			continue
		}
		b.WriteString("> " + l + "\n")
	}
	if cut > 0 {
		fmt.Fprintf(&b, "\n[Truncated: %d more bytes in %s.]\n", cut, source)
	}
	return b.String()
}
