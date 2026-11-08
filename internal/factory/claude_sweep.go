package factory

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// LaunchResidue is one Claude launch leftover found by the sweep after a phase
// ended without PostRun (for example a SIGKILLed adapter, acf-72): a TMPDIR
// launch dir or a projects entry named after it. Removed reports whether the
// sweep deleted it; only verified spill files and empty directories ever are.
// SpillFiles holds each removed spill's relative path and size, never its
// content. Detail says why a kept leftover was kept.
type LaunchResidue struct {
	Path       string      `json:"path"`
	Removed    bool        `json:"removed"`
	SpillFiles []SpillFile `json:"spill_files,omitempty"`
	Detail     string      `json:"detail,omitempty"`
}

// UnownedLaunch is a factory-claude-launch-* dir no run in this journal owns
// (another repository's run, possibly live, or an unidentifiable owner). It is
// reported only and never touched, nor are projects entries named after it.
type UnownedLaunch struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type sweepMode int

const (
	// sweepUnowned: the run is not in this journal; report, never touch.
	sweepUnowned sweepMode = iota
	// sweepSkip: a known run this sweep does not handle; ignore silently.
	sweepSkip
	// sweepRecord: the run may still have live descendants; record only.
	sweepRecord
	// sweepClean: no live process can own the leftovers; record and remove
	// verified spill files and empty directories through cleanTree.
	sweepClean
)

const claudeLaunchPrefix = "factory-claude-launch-"

// isClaudeLaunchName reports whether name is exactly what privateLaunchDir
// creates for Claude: the prefix plus os.MkdirTemp's decimal random suffix.
func isClaudeLaunchName(name string) bool {
	suffix, ok := strings.CutPrefix(name, claudeLaunchPrefix)
	if !ok || suffix == "" || len(suffix) > 20 {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// maxLaunchPhases bounds how many phase output dirs the owner lookup reads.
const maxLaunchPhases = 64

// launchOwner returns the run id recorded in a launch dir's private inputs
// (inputs/outputs/<phase>/context.json, written by privateInputs). It opens
// the launch dir as an os.Root proven to be the real directory, requires every
// path component to be a real directory or regular file (no symlinks), reads
// each context with a bounded read, and fails closed unless all contexts name
// one nonempty run id.
func launchOwner(launch string) (string, error) {
	root, err := os.OpenRoot(launch)
	if err != nil {
		return "", err
	}
	defer root.Close()
	lfi, err := os.Lstat(launch)
	if err != nil {
		return "", err
	}
	rfi, err := root.Stat(".")
	if err != nil {
		return "", err
	}
	if !lfi.IsDir() || !os.SameFile(lfi, rfi) {
		return "", fmt.Errorf("%s is not a real directory", launch)
	}
	outputs := filepath.Join("inputs", "outputs")
	for _, rel := range []string{"inputs", outputs} {
		fi, err := root.Lstat(rel)
		if err != nil {
			return "", fmt.Errorf("owner not identifiable: %w", err)
		}
		if !fi.IsDir() {
			return "", fmt.Errorf("owner not identifiable: %s is %s", rel, fi.Mode().Type())
		}
	}
	phases, err := fs.ReadDir(root.FS(), filepath.ToSlash(outputs))
	if err != nil {
		return "", fmt.Errorf("owner not identifiable: %w", err)
	}
	if len(phases) > maxLaunchPhases {
		return "", fmt.Errorf("owner not identifiable: more than %d phase dirs", maxLaunchPhases)
	}
	owner := ""
	for _, phase := range phases {
		if !phase.IsDir() {
			continue
		}
		rel := filepath.Join(outputs, phase.Name(), "context.json")
		fi, err := root.Lstat(rel)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !fi.Mode().IsRegular() {
			return "", fmt.Errorf("owner not identifiable: %s is %s", rel, fi.Mode().Type())
		}
		id, err := readContextRunID(root, rel, fi)
		if err != nil {
			return "", fmt.Errorf("owner not identifiable: %s: %w", rel, err)
		}
		if owner != "" && owner != id {
			return "", fmt.Errorf("owner not identifiable: contexts name runs %s and %s", owner, id)
		}
		owner = id
	}
	if owner == "" {
		return "", errors.New("owner not identifiable: no phase context")
	}
	return owner, nil
}

// readContextRunID reads run.id from a context file already Lstat'd as a
// regular file, refusing it if the opened file is not that same file.
func readContextRunID(root *os.Root, rel string, want os.FileInfo) (string, error) {
	f, err := root.Open(rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	got, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !got.Mode().IsRegular() || !os.SameFile(want, got) {
		return "", errors.New("changed during read")
	}
	data, err := io.ReadAll(io.LimitReader(f, outputLimit+1))
	if err != nil {
		return "", err
	}
	if len(data) > outputLimit {
		return "", errors.New("context exceeds size limit")
	}
	var phase struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	if err := json.Unmarshal(data, &phase); err != nil {
		return "", err
	}
	if phase.Run.ID == "" {
		return "", errors.New("context has no run id")
	}
	return phase.Run.ID, nil
}

// sweepClaudeLaunches finds factory-claude-launch-* dirs in TMPDIR and the
// Claude projects entries named after them, as left behind when a phase ended
// without PostRun. mode decides, per owning run id, what may happen:
//
//   - a dir whose owner is unknown, unidentifiable or not in this journal, or
//     that is not a real directory, is reported in unowned and never touched;
//     its projects entries are not even looked up;
//   - sweepRecord records the dir and its projects entries as kept residue;
//   - sweepClean removes them through the same os.Root audit PostRun uses:
//     spills only in the exact reserved entry (cleanProjectEntry), empty
//     directories otherwise (removeEmptyTree). Anything else fails that
//     audit, is kept and recorded with the reason. The launch dir is removed
//     only when it is empty and all of its projects entries were removed, so
//     the owner of a kept entry stays identifiable.
//
// Nothing is ever deleted with RemoveAll and no symlink is followed. The
// projects dir is read only once an owned launch dir is found.
func sweepClaudeLaunches(mode func(runID string) sweepMode) (map[string][]LaunchResidue, []UnownedLaunch, error) {
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return nil, nil, err
	}
	found := map[string][]LaunchResidue{}
	var unowned []UnownedLaunch
	var projects string
	var projectEntries []os.DirEntry
	var projectsErr error
	listed := false
	for _, e := range entries {
		if !isClaudeLaunchName(e.Name()) {
			continue
		}
		launch := filepath.Join(tmp, e.Name())
		// ReadDir reports the entry's own type: a symlink is never a dir.
		if !e.IsDir() {
			unowned = append(unowned, UnownedLaunch{Path: launch, Reason: fmt.Sprintf("not a real directory (%s); left untouched", e.Type())})
			continue
		}
		id, err := launchOwner(launch)
		if err != nil {
			unowned = append(unowned, UnownedLaunch{Path: launch, Reason: err.Error()})
			continue
		}
		m := mode(id)
		switch m {
		case sweepSkip:
			continue
		case sweepUnowned:
			unowned = append(unowned, UnownedLaunch{Path: launch, Reason: "run " + id + " is not in this journal"})
			continue
		}
		if !listed {
			listed = true
			projects, projectsErr = claudeProjectsDir()
			if projectsErr == nil {
				projectEntries, projectsErr = os.ReadDir(projects)
				if os.IsNotExist(projectsErr) {
					projectsErr = nil
				}
			}
		}
		if projectsErr != nil {
			found[id] = append(found[id], LaunchResidue{Path: launch, Detail: "kept: cannot audit Claude projects entries: " + projectsErr.Error()})
			continue
		}
		token := sanitiseProjectName(e.Name())
		reserved := sanitiseProjectName(launch)
		allRemoved := true
		for _, p := range projectEntries {
			if !ownsProjectEntry(p.Name(), token) {
				continue
			}
			path := filepath.Join(projects, p.Name())
			r := LaunchResidue{Path: path}
			if m != sweepClean {
				r.Detail = "kept: owning run may still have live descendants"
				allRemoved = false
				found[id] = append(found[id], r)
				continue
			}
			// cleanTree refuses symlinks and non-directories itself.
			var err error
			if p.Name() == reserved {
				r.SpillFiles, err = cleanProjectEntry(path)
			} else {
				err = removeEmptyTree(path)
			}
			if err != nil {
				r.Detail = "kept: " + err.Error()
				allRemoved = false
			} else {
				r.Removed = true
			}
			found[id] = append(found[id], r)
		}
		r := LaunchResidue{Path: launch}
		switch {
		case m != sweepClean:
			r.Detail = "kept: owning run may still have live descendants"
		case !allRemoved:
			r.Detail = "kept: identifies the owner of a kept projects entry"
		default:
			if err := removeEmptyTree(launch); err != nil {
				r.Detail = "kept: " + err.Error()
			} else {
				r.Removed = true
			}
		}
		found[id] = append(found[id], r)
	}
	return found, unowned, nil
}

// mergeLaunchResidue records found leftovers on a run, one record per path.
// A path seen again replaces its record but keeps earlier removed spills. It
// reports whether anything changed, so repeated sweeps do not rejournal.
func mergeLaunchResidue(have, found []LaunchResidue) ([]LaunchResidue, bool) {
	changed := false
	for _, f := range found {
		i := slices.IndexFunc(have, func(h LaunchResidue) bool { return h.Path == f.Path })
		if i < 0 {
			have = append(have, f)
			changed = true
			continue
		}
		if have[i].Removed == f.Removed && have[i].Detail == f.Detail && len(f.SpillFiles) == 0 {
			continue
		}
		f.SpillFiles = append(slices.Clone(have[i].SpillFiles), f.SpillFiles...)
		have[i] = f
		changed = true
	}
	return have, changed
}

// launchResidueDetail summarises a sweep for the run's event journal.
func launchResidueDetail(found []LaunchResidue) string {
	removed, kept, spills := 0, 0, 0
	for _, r := range found {
		if r.Removed {
			removed++
		} else {
			kept++
		}
		spills += len(r.SpillFiles)
	}
	return fmt.Sprintf("Claude launch leftovers swept: %d removed (%d spill files), %d kept as residue", removed, spills, kept)
}
