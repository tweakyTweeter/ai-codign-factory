package factory

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// claudeProjectsDir is where Claude keeps per-working-directory state:
// $CLAUDE_CONFIG_DIR/projects when set, else ~/.claude/projects. A relative
// base is refused, since it would not name the directory Claude uses.
func claudeProjectsDir() (string, error) {
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".claude")
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("claude config dir %q is not absolute", base)
	}
	return filepath.Join(base, "projects"), nil
}

// removeEmptyTree removes a directory tree that contains nothing but
// directories. It never follows symlinks (WalkDir uses Lstat), and any entry
// that is not a directory, a symlink included, fails closed before anything
// is removed. Removal is depth-first with rmdir, which refuses non-empty
// directories and non-directories, so a file appearing mid-walk also fails closed. Never replace
// this with os.RemoveAll: it runs under the user's real Claude config dir.
func removeEmptyTree(path string) error {
	_, err := cleanTree(path, false)
	return err
}

// cleanProjectEntry removes this launch's projects entry when it holds only
// directories and Claude's tool-result spills, regular files at exactly
// <session-uuid>/tool-results/<name>. It returns each removed spill's path
// relative to the entry and its size (never its content). Anything else, a
// symlink or special file included, fails closed before anything is removed.
func cleanProjectEntry(path string) ([]SpillFile, error) {
	return cleanTree(path, true)
}

// isSpillPath reports whether rel (relative to a projects entry) is where
// Claude spills a large tool result: <session-uuid>/tool-results/<name>.
func isSpillPath(rel string) bool {
	parts := strings.Split(rel, string(filepath.Separator))
	return len(parts) == 3 && isSessionID(parts[0]) && parts[1] == "tool-results" && parts[2] != "" && parts[2] != "." && parts[2] != ".."
}

// isSessionID reports whether s has the 8-4-4-4-12 hex shape of a Claude
// session id.
func isSessionID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
				return false
			}
		}
	}
	return true
}

// cleanTree audits path without following symlinks, then removes it: spill
// files (only when allowSpill) through an os.Root, which cannot escape the
// entry even if a directory is swapped for a symlink, then each directory
// depth-first with rmdir. Nothing is removed unless the whole tree passes.
func cleanTree(path string, allowSpill bool) ([]SpillFile, error) {
	var dirs, files []string
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		rel, relErr := filepath.Rel(path, p)
		if allowSpill && relErr == nil && d.Type().IsRegular() && isSpillPath(rel) {
			files = append(files, rel)
			return nil
		}
		return fmt.Errorf("%s is not empty: %s is %s", path, p, d.Type())
	})
	if err != nil {
		return nil, err
	}
	var spills []SpillFile
	if len(files) > 0 {
		root, err := os.OpenRoot(path)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		for _, rel := range files {
			fi, err := root.Lstat(rel)
			if err != nil {
				return spills, err
			}
			if !fi.Mode().IsRegular() {
				return spills, fmt.Errorf("%s changed type during removal", filepath.Join(path, rel))
			}
			if err := root.Remove(rel); err != nil {
				return spills, err
			}
			spills = append(spills, SpillFile{Path: rel, Size: fi.Size()})
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		fi, err := os.Lstat(dirs[i])
		if err != nil {
			return spills, err
		}
		if !fi.IsDir() {
			return spills, fmt.Errorf("%s changed type during removal", dirs[i])
		}
		if err := removeDirOnly(dirs[i]); err != nil {
			return spills, err
		}
	}
	return spills, nil
}

// ReserveSpillDir pre-creates this launch's projects entry, the directory
// Claude names after the sanitised launch path and spills large tool results
// into, so that it can be granted read-only to the model. The launch path is
// already symlink-resolved (privateLaunchDir), which is the cwd Claude sees.
// An existing entry (stale or foreign) fails closed and is left alone.
// release removes the entry if it still holds only empty directories; it is
// for runs that end before PostRun audits the entry.
func (claudeDriver) ReserveSpillDir(launch string) (string, func() error, error) {
	if err := checkLaunchPathLength(launch); err != nil {
		return "", nil, err
	}
	projects, err := claudeProjectsDir()
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(projects, 0700); err != nil {
		return "", nil, err
	}
	dir := filepath.Join(projects, sanitiseProjectName(launch))
	if err := os.Mkdir(dir, 0700); err != nil {
		return "", nil, fmt.Errorf("cannot reserve Claude projects entry %s: %w", dir, err)
	}
	release := func() error {
		if _, err := os.Lstat(dir); os.IsNotExist(err) {
			return nil
		}
		return removeEmptyTree(dir)
	}
	return dir, release, nil
}

// sanitiseProjectName mirrors how Claude names a projects entry: every
// character other than an ASCII letter or digit becomes '-'.
func sanitiseProjectName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, s)
}

// ownsProjectEntry reports whether a projects entry belongs to this launch.
// The entry is the sanitised absolute launch path, which always ends with the
// sanitised base name. A plain substring match is not enough: MkdirTemp
// suffixes vary in length, so ours ("...-123456") is a substring of a
// concurrent launch's ("...-1234567"), which must never be touched.
func ownsProjectEntry(name, token string) bool {
	return name == token || strings.HasSuffix(name, "-"+token)
}

// PostRun audits the private launch dir and the per-launch-dir state Claude
// creates under its projects directory. Nothing global may survive a phase:
// an entry holding only directories and tool-result spills is removed, with
// each spill's relative path and size recorded in out.SpillFiles; anything
// else is kept as evidence, recorded in out.Residue and fails the phase.
func (claudeDriver) PostRun(launch string, before map[string]string, out *PhaseOutcome) error {
	out.Runtime = "claude"
	var errs []error
	fail := func(err error) {
		if out.FailureReason == "" {
			out.FailureReason = "isolation_mismatch"
		}
		errs = append(errs, err)
	}
	if err := checkLaunchPathLength(launch); err != nil {
		fail(err)
	}
	after, err := treeManifest(launch)
	if err != nil {
		fail(err)
	} else if !maps.Equal(before, after) {
		fail(errors.New("Claude changed its private launch dir"))
	}
	projects, err := claudeProjectsDir()
	if err != nil {
		fail(err)
		return errors.Join(errs...)
	}
	entries, err := os.ReadDir(projects)
	if err != nil && !os.IsNotExist(err) {
		fail(err)
	}
	token := sanitiseProjectName(filepath.Base(launch))
	for _, e := range entries {
		if !ownsProjectEntry(e.Name(), token) {
			continue
		}
		path := filepath.Join(projects, e.Name())
		spills, err := cleanProjectEntry(path)
		out.SpillFiles = append(out.SpillFiles, spills...)
		if err != nil {
			out.Residue = append(out.Residue, path)
			fail(fmt.Errorf("Claude wrote global state under %s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

// maxSanitisedProjectName is the longest sanitised project name Claude is
// known to keep verbatim; longer names may be truncated and hashed, which the
// suffix match could not recognise.
const maxSanitisedProjectName = 200

// removeDirOnly removes an empty directory with rmdir(2). Unlike os.Remove it
// never falls back to unlink, so a file or symlink swapped in after the Lstat
// check can never be deleted. The package is darwin/linux only (wait_*.go).
func removeDirOnly(path string) error { return syscall.Rmdir(path) }

// checkLaunchPathLength fails closed when the launch path, as given or with
// symlinks resolved, would yield a projects entry name Claude may shorten.
func checkLaunchPathLength(launch string) error {
	paths := []string{launch}
	if resolved, err := filepath.EvalSymlinks(launch); err == nil && resolved != launch {
		paths = append(paths, resolved)
	}
	for _, p := range paths {
		if len(sanitiseProjectName(p)) > maxSanitisedProjectName {
			return fmt.Errorf("launch path %q is too long to audit Claude residue (%d > %d)", p, len(sanitiseProjectName(p)), maxSanitisedProjectName)
		}
	}
	return nil
}
