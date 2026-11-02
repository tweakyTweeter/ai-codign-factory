package factory

import (
	"context"
	"os"
	"path/filepath"
)

type copilotDriver struct{}

const copilotNotice = "copied explicit profile; explicit runtime tool policy overrides profile tool declarations; cwd is a private temp launch dir outside any checkout with disableAllHooks=true; global extensions still load; no live run evidence directory grant; only candidate and private input/profile copies are exposed, automatic system-temp access disabled; private inputs/profile/settings are checked for mutation; write approval is not recursively path-scoped; not a sandbox"

func (copilotDriver) Name() string   { return "copilot" }
func (copilotDriver) Label() string  { return "Copilot" }
func (copilotDriver) Notice() string { return copilotNotice }

func (copilotDriver) LaunchSettings(launch string) (LaunchSettings, error) {
	dir := filepath.Join(launch, ".github", "copilot")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return LaunchSettings{}, err
	}
	file := filepath.Join(dir, "settings.local.json")
	if err := writeExclusive(file, []byte(copilotLaunchSettings)); err != nil {
		return LaunchSettings{}, err
	}
	return LaunchSettings{SealDir: ".github", File: file, Content: copilotLaunchSettings}, nil
}

func (copilotDriver) ProfileLaunchCopy(launch, agent string, profile []byte) (string, error) {
	dir := filepath.Join(launch, "profile-snapshot", ".github", "agents")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, agent+".agent.md")
	return path, writeExclusive(path, profile)
}

// Tools maps v2 grants onto Copilot tool names; v1 keeps its explicit lists.
func (copilotDriver) Tools(p PhasePolicy, _ ToolRoots) (DriverTools, error) {
	if p.Copilot != nil {
		return DriverTools{Available: p.Copilot.AvailableTools, Allowed: p.Copilot.AllowTools}, nil
	}
	t := DriverTools{Available: []string{"view", "rg", "glob"}, Allowed: []string{}}
	for _, g := range p.Grants {
		if g == "write" {
			t.Available = append(t.Available, "create", "edit", "apply_patch")
			t.Allowed = append(t.Allowed, "write")
		}
	}
	return t, nil
}

func (copilotDriver) Env() ([]string, bool, []string) {
	return []string{"COPILOT_ALLOW_ALL=false"}, true, nil
}

func (copilotDriver) Version(context.Context, PhasePolicy, string, []string, bool) (string, error) {
	return "", nil
}

func (copilotDriver) Manifest(m ManifestBase, notice string) any {
	var policy any = m.Policy
	if m.Policy.Copilot != nil {
		policy = *m.Policy.Copilot
	}
	return struct {
		Policy          any    `json:"policy"`
		SHA256          string `json:"source_profile_sha256"`
		ResolvedProfile string `json:"resolved_source_profile"`
		LaunchDir       string `json:"launch_dir"`
		LaunchSettings  string `json:"launch_settings"`
		Worktree        string `json:"worktree"`
		Notice          string `json:"notice"`
	}{policy, m.SHA256, m.ResolvedProfile, m.LaunchDir, m.Settings.Content, m.Worktree, notice}
}

func (copilotDriver) Argv(p PhasePolicy, in LaunchInputs) []string {
	modelProfile := filepath.Join(in.Launch, "profile-snapshot")
	argv := []string{p.Executable, "--agent", p.Agent, "--add-dir", in.Worktree, "--add-dir", modelProfile,
		"--disallow-temp-dir", "--no-custom-instructions",
		"--no-ask-user", "--disable-builtin-mcps", "--no-auto-update", "--stream", "off",
		"--silent", "--output-format", "json", "--available-tools"}
	argv = append(argv, in.Tools.Available...)
	argv = append(argv, "--deny-tool", "shell")
	if p.Role != "dev" {
		argv = append(argv, "--deny-tool", "write")
	}
	for _, approval := range in.Tools.Allowed {
		argv = append(argv, "--allow-tool", approval)
	}
	return append(argv, "-p", in.Instructions)
}

func (copilotDriver) Final(raw []byte, _ LaunchInputs, _ *PhaseOutcome) (string, error) {
	return copilotFinal(raw)
}

func (copilotDriver) PostRun(string, map[string]string, *PhaseOutcome) error { return nil }

// ReserveSpillDir is a no-op: Copilot has no per-launch spill dir to grant.
func (copilotDriver) ReserveSpillDir(string) (string, func() error, error) {
	return "", func() error { return nil }, nil
}
