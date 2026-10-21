package factory

import (
	"context"
	"errors"
)

// claudeDriver is a placeholder until the Claude driver lands; it fails closed
// before anything is launched.
type claudeDriver struct{}

func (claudeDriver) Name() string   { return "claude" }
func (claudeDriver) Label() string  { return "Claude" }
func (claudeDriver) Notice() string { return "" }

func (claudeDriver) LaunchSettings(string) (LaunchSettings, error) {
	return LaunchSettings{}, errors.New("claude driver not implemented")
}

func (claudeDriver) ProfileLaunchCopy(string, string, []byte) (string, error) { return "", nil }

func (claudeDriver) Tools(PhasePolicy, ToolRoots) (DriverTools, error) { return DriverTools{}, nil }

func (claudeDriver) Env() ([]string, bool, []string) { return nil, false, nil }

func (claudeDriver) Version(context.Context, PhasePolicy, string, []string, bool) (string, error) {
	return "", nil
}

func (claudeDriver) Manifest(ManifestBase, string) any { return nil }

func (claudeDriver) Argv(PhasePolicy, LaunchInputs) []string { return nil }

func (claudeDriver) Final([]byte, LaunchInputs, *PhaseOutcome) (string, error) { return "", nil }

func (claudeDriver) PostRun(string, map[string]string, *PhaseOutcome) error { return nil }
