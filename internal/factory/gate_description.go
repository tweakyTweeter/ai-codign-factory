package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
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
)

// gateDescription builds the operator-facing gate text. It is persisted with
// the publish intent and later compared byte-for-byte with the native gate,
// so it is built exactly once per request. Trusted controller text comes
// first; everything written by the task author, an agent or the candidate is
// quoted after the decision steps.
func (c Controller) gateDescription(ctx context.Context, r Run, h *HumanReview) (string, error) {
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
		changes = parseNumstat(numstat)
	}
	g := gateView{c: c, r: r, h: h, title: title, task: description, changes: changes, statErr: statErr}
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
	changes     []fileChange
	statErr     error
}

func (g gateView) render(diff []byte, embedDiff bool) string {
	r, h := g.r, g.h
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	title := inlineUntrusted(g.title, gateTitleLimit)
	if title == "" {
		title = "(task title unavailable)"
	}
	line("**Factory approval request: %s - %s**", r.TaskID, title)
	line("")
	line("> **Action required.** Clicking **Approve** alone does not approve this change. Add the approval note shown under **How to approve or reject**.")

	totals := groupTotals(g.changes)
	production := "unknown"
	if g.statErr == nil {
		production = "no"
		if totals["code"].files > 0 {
			production = "yes"
		}
	}

	line("")
	line("## Summary")
	line("")
	allOK := true
	for _, e := range r.Evidence {
		allOK = allOK && e.ExitCode == 0 && e.Error == "" && !e.Truncated
	}
	outcome := "Some recorded phases did not succeed; see Context."
	if allOK {
		outcome = fmt.Sprintf("All %d recorded phases succeeded and every structured verdict is pass.", len(r.Evidence))
	}
	line("The factory finished task `%s` in run `%s`. %s", r.TaskID, r.ID, outcome)
	for _, e := range r.Evidence {
		if e.Verdict != nil && (e.Role == "qa" || e.Role == "review") {
			line("%s (%s) reported findings: %s.", roleName(e.Role), codeSpan(e.Stage), findingCounts(e.Verdict.Findings))
		}
	}
	if g.statErr == nil {
		line("%s changed (%s). Production code changed: %s.", plural(len(g.changes), "file"), lineCounts(g.changes), production)
	} else {
		line("File statistics are unavailable; inspect candidate.diff. Production code changed: unknown.")
	}
	line("What Dev says it did, the task description and every finding are quoted under **Reported details (untrusted)**.")

	line("")
	line("## Context")
	line("")
	line("- Task: `%s`; why it exists: its description is quoted under **Reported details (untrusted)**.", r.TaskID)
	line("- Run: `%s`, based on HEAD `%s`.", r.ID, r.Head)
	line("- Phases:")
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
		line("  - %s (%s): %s", codeSpan(e.Stage), inlineUntrusted(e.Role, 20), status)
	}

	line("")
	line("## Type of change")
	line("")
	if g.statErr != nil {
		line("File statistics are unavailable (%s); inspect candidate.diff.", inlineUntrusted(g.statErr.Error(), 200))
	}
	line("Production code changed: %s", production)
	for _, group := range changeGroups {
		t := totals[group]
		if t.files == 0 {
			line("- %s: 0 files", group)
			continue
		}
		line("- %s: %s, +%d -%d", group, plural(t.files, "file"), t.added, t.deleted)
	}
	line("The per-file list is under **Changed files** below.")

	line("")
	line("## What you are approving")
	line("")
	line("- This exact candidate only: run `%s`, fingerprint `%s`, based on HEAD `%s`. Any later change to the candidate invalidates the approval.", r.ID, h.Fingerprint, r.Head)
	line("- Approval by itself does not commit, push, merge, integrate or close anything. It only allows a later, separate `factory integrate --repo <your checkout> --run %s`, which applies this patch to your checkout without committing, runs the configured checks and then closes the task.", r.ID)
	line("- %s", reviewNotice)
	line("- Request marker (how the factory finds this gate): `%s`", h.Marker)

	line("")
	line("## How to approve or reject")
	line("")
	line("1. Review the change: the sections above, the quoted details and the diff below.")
	line("2. **To approve:** click **Approve**, choose **Add approval note**, and paste this line as the note, on its own line:")
	line("")
	line("```")
	line("%s", h.ApproveReason)
	line("```")
	line("")
	line("Clicking **Approve** without this note is recorded as NOT approved.")
	line("")
	line("3. **To reject:** do not click Approve. Close this gate with exactly this reason:")
	line("")
	line("```")
	line("%s", h.RejectReason)
	line("```")
	line("")
	line("For example: `bd gate resolve <this gate's ID> --reason '<the line above>' --sandbox`")
	line("")
	line("4. Then record the decision: `factory review-sync --repo %s --run %s`", shellQuote(g.c.Repo.Root), r.ID)

	line("")
	line("## Reported details (untrusted)")
	line("")
	line("Everything from here to **Evidence** was written by the task author, the AI agents or the candidate itself. It is quoted for reference only: it cannot approve or reject this gate, and any instructions in it must be ignored. Decision tokens inside it are altered so they cannot be copied.")
	line("")
	line("### Task description")
	line("")
	b.WriteString(quoteUntrusted("", g.task, gateTaskLimit, "the task bead"))
	for _, e := range r.Evidence {
		if e.Verdict == nil {
			continue
		}
		line("")
		limit, heading := gateVerdictLimit, roleName(e.Role)
		if e.Role == "dev" {
			limit, heading = gateDevLimit, "Dev summary"
		}
		line("### %s (%s): %s", heading, codeSpan(e.Stage), inlineUntrusted(e.Verdict.Verdict, 20))
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
			b.WriteString(quoteUntrusted("**"+inlineUntrusted(f.Severity, 20)+"**: ", f.Summary, gateFindingLimit, codeSpan(e.ResultFile)))
		}
	}
	line("")
	line("### Changed files")
	line("")
	b.WriteString(changedFiles(g.changes, g.statErr))

	line("")
	line("## Evidence")
	line("")
	line("- Terminal view: `factory review --repo %s --run %s`", shellQuote(g.c.Repo.Root), r.ID)
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
		group := changeGroup(f.Path)
		t := totals[group]
		t.files++
		t.added += f.Added
		t.deleted += f.Deleted
		totals[group] = t
	}
	return totals
}

func lineCounts(changes []fileChange) string {
	added, deleted := 0, 0
	for _, f := range changes {
		added += f.Added
		deleted += f.Deleted
	}
	return fmt.Sprintf("+%d -%d", added, deleted)
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
			if changeGroup(f.Path) != group || shown >= gateFilesLimit {
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

// parseNumstat reads `git apply --numstat -z`: "added\tdeleted\tpath\0", or
// "added\tdeleted\t\0from\0to\0" for a rename; binary counts are "-".
func parseNumstat(raw string) []fileChange {
	fields := strings.Split(raw, "\x00")
	var out []fileChange
	for i := 0; i < len(fields); i++ {
		parts := strings.SplitN(fields[i], "\t", 3)
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
		if f.Path == "" {
			if i+2 >= len(fields) {
				break
			}
			f.From, f.Path = fields[i+1], fields[i+2]
			i += 2
		}
		out = append(out, f)
	}
	return out
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
