package factory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

type Finding struct {
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
}

// Verdict is the adapter boundary. Exit zero alone is never review evidence.
type Verdict struct {
	Version     int       `json:"version"`
	Verdict     string    `json:"verdict"`
	Summary     string    `json:"summary"`
	Fingerprint string    `json:"fingerprint"`
	Findings    []Finding `json:"findings"`
}

func decodeStrict(data []byte, target any) error {
	if len(data) == 0 || len(data) > outputLimit {
		return errors.New("JSON evidence must contain 1 byte to 1 MiB")
	}
	if err := uniqueJSON(json.NewDecoder(bytes.NewReader(data)), 0); err != nil {
		return err
	}
	if err := exactFields(data, reflect.TypeOf(target)); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("evidence must contain exactly one JSON object")
	}
	return nil
}

// encoding/json otherwise accepts duplicate keys and case-insensitive field
// aliases, both undesirable for a machine-verdict trust boundary.
func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting exceeds 64")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return errors.New("duplicate or invalid JSON key")
			}
			seen[key] = true
			if err := uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = d.Token()
	return err
}

func exactFields(data []byte, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		var values map[string]json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			key := strings.Split(f.Tag.Get("json"), ",")[0]
			if key == "" {
				key = f.Name
			}
			fields[key] = f.Type
		}
		for key, value := range values {
			field, ok := fields[key]
			if !ok {
				return fmt.Errorf("unknown JSON field %q", key)
			}
			if err := exactFields(value, field); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := exactFields(value, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func readBounded(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > outputLimit {
		return nil, errors.New("evidence must be a regular file of at most 1 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, outputLimit+1))
	if err == nil && len(b) > outputLimit {
		err = errors.New("evidence exceeds 1 MiB")
	}
	return b, err
}

// readLimited reads a regular file of at most limit bytes. readBounded is the
// 1 MiB form used for evidence the controller parses.
func readLimited(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("evidence must be a regular file of at most %d MiB", limit>>20)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("evidence exceeds %d MiB", limit>>20)
	}
	return b, err
}

// digestFile returns digest(contents) of a regular file of at most limit bytes.
func digestFile(path string, limit int64) (string, error) {
	b, err := readLimited(path, limit)
	if err != nil {
		return "", err
	}
	return digest(b), nil
}

func readVerdict(path, fingerprint string) (Verdict, error) {
	var v Verdict
	b, err := readBounded(path)
	if err != nil {
		return v, err
	}
	if err := decodeStrict(b, &v); err != nil {
		return v, err
	}
	if v.Version != 1 || strings.TrimSpace(v.Summary) == "" || len(v.Summary) > 16384 || v.Findings == nil || len(v.Findings) > 1000 {
		return v, errors.New("invalid verdict schema/version/summary/findings")
	}
	if v.Fingerprint != fingerprint {
		return v, errors.New("verdict fingerprint does not match current patch")
	}
	for _, f := range v.Findings {
		if strings.TrimSpace(f.Summary) == "" || len(f.Summary) > 16384 {
			return v, errors.New("invalid finding summary")
		}
		switch f.Severity {
		case "info", "warning":
		case "blocking":
			return v, errors.New("blocking review finding")
		default:
			return v, errors.New("unknown finding severity")
		}
	}
	if v.Verdict != "pass" {
		return v, fmt.Errorf("verdict %q is not pass", v.Verdict)
	}
	return v, nil
}

// Fingerprint binds HEAD, index, file modes and contents, including ignored and
// untracked files. No temporary index or object writes are needed. Large trees
// fail closed rather than accepting partial evidence. This is detection, NOT a
// filesystem sandbox: commands remain explicitly trusted local code.
func Fingerprint(ctx context.Context, dir string) (string, error) {
	h := sha256.New()
	for _, args := range [][]string{{"rev-parse", "HEAD"}, {"diff", "--cached", "--binary", "--no-ext-diff", "--no-textconv", "HEAD"}} {
		b, err := gitOutput(ctx, dir, args...)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%d:%s", len(b), b)
	}
	var total int64
	count := 0
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == ".git" && entry.IsDir() {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		count++
		if count > 100000 {
			return errors.New("fingerprint exceeds 100000 files")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%d:%s:%s:", len(rel), rel, info.Mode())
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "%d:%s", len(target), target)
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported file in fingerprint: %s", rel)
		}
		total += info.Size()
		if total > 128<<20 {
			return errors.New("fingerprint exceeds 128 MiB")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%d:", info.Size())
		n, copyErr := io.Copy(h, io.LimitReader(f, (128<<20)+1))
		closeErr := f.Close()
		if n != info.Size() {
			return fmt.Errorf("file changed during fingerprint: %s", rel)
		}
		return errors.Join(copyErr, closeErr)
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (c Controller) workflow(ctx context.Context, run *Run, cfg Config, env []string, profile string) error {
	selected := profile == ""
	type artifact struct {
		Name      string `json:"name"`
		Role      string `json:"role"`
		OutputDir string `json:"output_dir"`
	}
	prior := []artifact{}
	for _, p := range cfg.Workflow.Phases {
		if p.Role == "specialist" && p.Profile != profile {
			continue
		}
		if p.Role == "specialist" {
			selected = true
		}
		if err := c.owned(ctx, *run, cfg); err != nil {
			return err
		}
		before, err := Fingerprint(ctx, run.Worktree)
		if err != nil {
			return err
		}
		output := filepath.Join(run.Dir, "outputs", p.Name)
		if err := c.save(run, p.Name+"_preparing", "intent: prepare phase output directory", false); err != nil {
			return err
		}
		if err := os.MkdirAll(output, 0700); err != nil {
			return err
		}
		resultPath := filepath.Join(output, "result.json")
		phaseContext := filepath.Join(output, "context.json")
		run.Stage, run.Role = p.Name, p.Role
		contextData, err := json.MarshalIndent(struct {
			Run         Run        `json:"run"`
			PromptFile  string     `json:"prompt_file"`
			TaskFile    string     `json:"task_file"`
			Prior       []artifact `json:"prior_phases"`
			Fingerprint string     `json:"candidate_fingerprint"`
		}{*run, filepath.Join(run.Dir, "prompt.txt"), filepath.Join(run.Dir, "task.json"), prior, before}, "", "  ")
		if err != nil {
			return err
		}
		if err := writeExclusive(phaseContext, contextData); err != nil {
			return err
		}
		if err := sealArtifacts(ctx, run, filepath.Join("outputs", p.Name, "context.json")); err != nil {
			return err
		}
		phaseEnv := append(append([]string{}, env...),
			"FACTORY_PHASE="+p.Name, "FACTORY_ROLE="+p.Role,
			"FACTORY_OUTPUT_DIR="+output, "FACTORY_RESULT_FILE="+resultPath,
			"FACTORY_PHASE_CONTEXT_FILE="+phaseContext,
			"FACTORY_PATCH_FINGERPRINT="+before, "FACTORY_PROFILE="+p.Profile,
			// The adapter stops its runtime before this timeout so its post-run
			// audit still runs; the controller's SIGKILL would skip it (acf-67).
			phaseTimeoutEnv+"="+p.Command.Timeout)
		if err := c.owned(ctx, *run, cfg); err != nil {
			return err
		}
		if err := c.command(ctx, run, "phase-"+p.Name, p.Command, phaseEnv); err != nil {
			return err
		}
		after, err := Fingerprint(ctx, run.Worktree)
		if err != nil {
			return err
		}
		if p.Role != "dev" && before != after {
			return fmt.Errorf("nonwriting %s phase modified worktree/HEAD/index", p.Role)
		}
		head, err := gitOutput(ctx, run.Worktree, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		if head != run.Head {
			return errors.New("phase changed HEAD; automatic commits are not permitted")
		}
		if p.Role == "dev" {
			v, err := readVerdict(resultPath, after)
			e := &run.Evidence[len(run.Evidence)-1]
			e.ResultFile, e.Verdict, e.Fingerprint = resultPath, &v, after
			if err != nil {
				return fmt.Errorf("dev evidence: %w", err)
			}
			if err := c.save(run, p.Name+"_accepted", "Dev completion bound to candidate; checks/independent QA/review still required", false); err != nil {
				return err
			}
			for i, check := range cfg.Checks {
				if err := c.owned(ctx, *run, cfg); err != nil {
					return err
				}
				if err := c.command(ctx, run, fmt.Sprintf("check-%02d", i+1), check, env); err != nil {
					return err
				}
			}
		}
		if p.Role == "qa" || p.Role == "review" {
			v, err := readVerdict(resultPath, before)
			e := &run.Evidence[len(run.Evidence)-1]
			e.ResultFile, e.Verdict, e.Fingerprint = resultPath, &v, before
			if err != nil {
				return errors.Join(fmt.Errorf("%s evidence: %w", p.Role, err), c.save(run, p.Name+"_rejected", "invalid or nonpassing structured evidence", false))
			}
			if p.Role == "qa" {
				run.QAFingerprint = before
			}
			if p.Role == "review" && run.QAFingerprint != before {
				return errors.New("patch changed after QA")
			}
			if err := c.save(run, p.Name+"_accepted", "structured verdict accepted for "+before, false); err != nil {
				return err
			}
		}
		prior = append(prior, artifact{p.Name, p.Role, output})
	}
	if !selected {
		return fmt.Errorf("unknown specialist profile %q", profile)
	}
	current, err := Fingerprint(ctx, run.Worktree)
	if err != nil {
		return err
	}
	if current != run.QAFingerprint {
		return errors.New("patch changed after QA/review")
	}
	run.Fingerprint = current
	return nil
}
