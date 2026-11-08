package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Integrity is controller-journaled detection, not protection from processes
// with the same OS identity and access to the journal.
type Integrity struct {
	Version int               `json:"version"`
	Seals   map[string]string `json:"seals"`
}

func artifactDigest(ctx context.Context, root, name string) (string, error) {
	if filepath.IsAbs(name) || filepath.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid evidence path")
	}
	// Reject symlink ancestors as well as symlink leaves.
	for p := name; p != "."; p = filepath.Dir(p) {
		info, err := os.Lstat(filepath.Join(root, p))
		if err != nil {
			if os.IsNotExist(err) && p == name {
				continue
			}
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink in evidence path: %s", p)
		}
	}
	var records []string
	var total int64
	err := filepath.WalkDir(filepath.Join(root, name), func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) && path == filepath.Join(root, name) {
			records = append(records, "absent")
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(records) >= 100000 {
			return errors.New("evidence exceeds 100000 entries")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		content := ""
		if !entry.IsDir() {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("nonregular evidence: %s", rel)
			}
			total += info.Size()
			if total > 128<<20 {
				return errors.New("evidence exceeds 128 MiB")
			}
			// Sealed evidence includes a runtime's raw event stream, which may
			// exceed the 1 MiB that parsed evidence is held to.
			content, err = digestFile(path, runtimeStreamLimit)
			if err != nil {
				return err
			}
		}
		records = append(records, fmt.Sprintf("%d:%s:%s:%s", len(rel), rel, info.Mode(), content))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(records)
	return digest([]byte(strings.Join(records, "\n"))), nil
}

func verifyIntegrity(ctx context.Context, r Run) error {
	if r.Integrity == nil {
		return errors.New("run has no controller evidence seals; new verified run required")
	}
	if r.Integrity.Version != 1 || len(r.Integrity.Seals) == 0 {
		return errors.New("invalid controller evidence seals")
	}
	for name, want := range r.Integrity.Seals {
		got, err := artifactDigest(ctx, r.Dir, name)
		if err != nil || got != want {
			return errors.Join(err, fmt.Errorf("evidence integrity changed: %s", name))
		}
	}
	return nil
}

func sealArtifacts(ctx context.Context, r *Run, names ...string) error {
	if r.Integrity == nil {
		r.Integrity = &Integrity{Version: 1, Seals: map[string]string{}}
	}
	// Never replace an existing seal with the newly observed content.
	if len(r.Integrity.Seals) > 0 {
		if err := verifyIntegrity(ctx, *r); err != nil {
			return err
		}
	}
	for _, name := range names {
		if _, exists := r.Integrity.Seals[name]; exists {
			continue
		}
		sum, err := artifactDigest(ctx, r.Dir, name)
		if err != nil {
			return err
		}
		r.Integrity.Seals[name] = sum
	}
	return nil
}
