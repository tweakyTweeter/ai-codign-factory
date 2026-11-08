package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Copy only sealed inputs and completed evidence, never the journal, active
// outputs, sibling runs or candidate. References point into the private copy.
func privateInputs(ctx context.Context, launch, contextPath, worktree string) (string, error) {
	data, err := readBounded(contextPath)
	if err != nil {
		return "", err
	}
	var phase struct {
		Run Run `json:"run"`
	}
	if err := json.Unmarshal(data, &phase); err != nil {
		return "", err
	}
	r := phase.Run
	if r.Worktree != worktree || !filepath.IsAbs(r.Dir) || r.Integrity == nil {
		return "", errors.New("Copilot requires controller-sealed phase inputs")
	}
	if err := verifyIntegrity(ctx, r); err != nil {
		return "", err
	}
	inputs := filepath.Join(launch, "inputs")
	if err := os.Mkdir(inputs, 0700); err != nil {
		return "", err
	}
	names := make([]string, 0, len(r.Integrity.Seals)+1)
	for name := range r.Integrity.Seals {
		names = append(names, name)
	}
	rel, err := filepath.Rel(r.Dir, contextPath)
	if err != nil {
		return "", err
	}
	names = append(names, rel)
	for _, name := range names {
		if _, err := artifactDigest(ctx, r.Dir, name); err != nil {
			return "", err
		}
		err := filepath.WalkDir(filepath.Join(r.Dir, name), func(path string, entry os.DirEntry, err error) error {
			if os.IsNotExist(err) && path == filepath.Join(r.Dir, name) {
				return nil // Task/claim snapshots can be explicitly sealed absent.
			}
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(r.Dir, path)
			if err != nil {
				return err
			}
			dest := filepath.Join(inputs, rel)
			if entry.IsDir() {
				return os.MkdirAll(dest, 0700)
			}
			// Parsed JSON evidence keeps the 1 MiB limit; other sealed files,
			// such as a runtime's raw event stream, may be up to
			// runtimeStreamLimit, the size they were sealed at.
			limit := int64(runtimeStreamLimit)
			if filepath.Ext(path) == ".json" {
				limit = outputLimit
			}
			b, err := readLimited(path, limit)
			if err != nil {
				return err
			}
			if filepath.Ext(path) == ".json" {
				b, err = rebaseEvidenceJSON(b, r.Dir, inputs, worktree)
				if err != nil {
					return err
				}
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
				return err
			}
			// Directory seals and individual context seals overlap.
			if existing, err := readLimited(dest, limit); err == nil {
				if !bytes.Equal(existing, b) {
					return errors.New("inconsistent private evidence copy")
				}
				return nil
			} else if !os.IsNotExist(err) {
				return err
			}
			return writeExclusive(dest, b)
		})
		if err != nil {
			return "", err
		}
	}
	if err := verifyIntegrity(ctx, r); err != nil {
		return "", err
	}
	return inputs, nil
}

func rebaseEvidenceJSON(data []byte, source, dest, worktree string) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var rebase func(any) any
	rebase = func(v any) any {
		switch v := v.(type) {
		case string:
			if v == worktree || strings.HasPrefix(v, worktree+string(filepath.Separator)) {
				return v
			}
			if v == source || strings.HasPrefix(v, source+string(filepath.Separator)) {
				return dest + strings.TrimPrefix(v, source)
			}
		case map[string]any:
			for key, item := range v {
				v[key] = rebase(item)
			}
		case []any:
			for i, item := range v {
				v[i] = rebase(item)
			}
		}
		return v
	}
	return json.Marshal(rebase(value))
}
