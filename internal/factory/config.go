package factory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Command is a trusted executable and literal arguments, never an implicit shell.
type Command struct {
	Argv    []string `json:"argv"`
	Timeout string   `json:"timeout"`
}

type Config struct {
	Version   int                `json:"version"`
	Worker    Command            `json:"worker"`
	Checks    []Command          `json:"checks"`
	Profiles  map[string]Command `json:"profiles,omitempty"`
	BeadsArgv []string           `json:"beads_argv,omitempty"`
	Workflow  *Workflow          `json:"workflow,omitempty"`
}

// Workflow is a single supervised sequence, not a second task scheduler.
type Workflow struct {
	Phases []Phase `json:"phases"`
}

type Phase struct {
	Name    string  `json:"name"`
	Role    string  `json:"role"`
	Command Command `json:"command"`
	Profile string  `json:"profile,omitempty"`
}

func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()
	return DecodeConfig(f)
}

func DecodeConfig(r io.Reader) (Config, error) {
	var c Config
	data, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if err != nil {
		return c, fmt.Errorf("read config: %w", err)
	}
	if len(data) > 1<<20 {
		return c, errors.New("config exceeds 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("decode config: %w", err)
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return c, errors.New("config must contain exactly one JSON object")
	}
	return validateConfig(c)
}

func validateConfig(c Config) (Config, error) {
	if c.Version != 1 {
		return c, errors.New("config version must be 1")
	}
	if c.Workflow == nil {
		if err := validateCommand(c.Worker); err != nil {
			return c, fmt.Errorf("worker: %w", err)
		}
	} else {
		if len(c.Worker.Argv) != 0 || len(c.Profiles) != 0 {
			return c, errors.New("workflow cannot also configure worker/profiles")
		}
		if err := validateWorkflow(*c.Workflow); err != nil {
			return c, err
		}
	}
	if len(c.Checks) == 0 || len(c.Checks) > 32 {
		return c, errors.New("configure between 1 and 32 quality checks")
	}
	for i, cmd := range c.Checks {
		if err := validateCommand(cmd); err != nil {
			return c, fmt.Errorf("check %d: %w", i, err)
		}
	}
	for name, cmd := range c.Profiles {
		if strings.TrimSpace(name) == "" {
			return c, errors.New("profile name is empty")
		}
		if err := validateCommand(cmd); err != nil {
			return c, fmt.Errorf("profile %s: %w", name, err)
		}
	}
	if c.BeadsArgv == nil {
		c.BeadsArgv = []string{"bd"}
	}
	if err := validateArgv(c.BeadsArgv); err != nil {
		return c, fmt.Errorf("beads_argv: %w", err)
	}
	return c, nil
}

func validateWorkflow(w Workflow) error {
	if len(w.Phases) < 4 || len(w.Phases) > 32 {
		return errors.New("workflow requires 4 to 32 phases")
	}
	names := map[string]bool{}
	step := "producer"
	for _, p := range w.Phases {
		if !safeName(p.Name) || names[p.Name] {
			return errors.New("phase names must be unique safe identifiers")
		}
		names[p.Name] = true
		if err := validateCommand(p.Command); err != nil {
			return fmt.Errorf("phase %s: %w", p.Name, err)
		}
		switch p.Role {
		case "producer":
			if step != "producer" {
				return errors.New("producer must be first and unique")
			}
			step = "planning"
		case "specialist":
			if step != "planning" || !safeName(p.Profile) {
				return errors.New("specialists require explicit profile and must precede dev")
			}
		case "dev":
			if step != "planning" {
				return errors.New("exactly one dev must follow producer/planners")
			}
			step = "qa"
		case "qa":
			if step != "qa" {
				return errors.New("qa must follow dev and checks")
			}
			step = "review"
		case "review":
			if step != "review" {
				return errors.New("review must follow independent qa")
			}
			step = "done"
		default:
			return fmt.Errorf("unknown phase role %q", p.Role)
		}
		if p.Role != "specialist" && p.Profile != "" {
			return errors.New("profile selection is only valid on specialist phases")
		}
	}
	if step != "done" {
		return errors.New("workflow requires producer, dev, qa, review")
	}
	return nil
}

func safeName(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validateArgv(argv []string) error {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" || strings.HasPrefix(argv[0], "-") {
		return errors.New("argv needs an executable")
	}
	for _, s := range argv {
		if strings.ContainsRune(s, 0) {
			return errors.New("argv contains NUL")
		}
	}
	return nil
}

func validateCommand(c Command) error {
	if err := validateArgv(c.Argv); err != nil {
		return err
	}
	d, err := time.ParseDuration(c.Timeout)
	if err != nil || d <= 0 || d > 24*time.Hour {
		return errors.New("timeout must be a positive duration of at most 24h")
	}
	return nil
}
