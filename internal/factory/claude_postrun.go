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
	var dirs []string
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return fmt.Errorf("%s is not empty: %s is %s", path, p, d.Type())
		}
		dirs = append(dirs, p)
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		fi, err := os.Lstat(dirs[i])
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s changed type during removal", dirs[i])
		}
		if err := removeDirOnly(dirs[i]); err != nil {
			return err
		}
	}
	return nil
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
// an empty residue tree is removed, anything else is kept as evidence,
// recorded in out.Residue and fails the phase.
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
		if err := removeEmptyTree(path); err != nil {
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
