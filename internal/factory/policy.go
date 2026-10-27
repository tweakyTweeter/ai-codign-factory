package factory

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
)

// PhasePolicy is the runtime-neutral view of a phase adapter policy. Version 1
// policies are Copilot-only and keep their original schema and validation.
type PhasePolicy struct {
	Runtime       string         `json:"runtime"`
	Executable    string         `json:"executable"`
	Role          string         `json:"role"`
	Agent         string         `json:"agent"`
	SourceProfile string         `json:"source_profile"`
	Grants        []string       `json:"grants,omitempty"`
	Model         string         `json:"model,omitempty"`
	Copilot       *CopilotPolicy `json:"-"`
}

type policyV2 struct {
	Version       int      `json:"version"`
	Runtime       string   `json:"runtime"`
	Executable    string   `json:"executable"`
	Role          string   `json:"role"`
	Agent         string   `json:"agent"`
	SourceProfile string   `json:"source_profile"`
	Grants        []string `json:"grants"`
	Model         string   `json:"model"`
}

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// LoadPhasePolicy decodes a v1 (Copilot) or v2 (runtime-neutral) policy and
// validates it against the controller-assigned role. Grants are explicit
// approvals, not a sandbox.
func LoadPhasePolicy(data []byte, role string) (PhasePolicy, error) {
	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return PhasePolicy{}, err
	}
	switch probe.Version {
	case 1:
		var p CopilotPolicy
		if err := decodeStrict(data, &p); err != nil {
			return PhasePolicy{}, err
		}
		if err := validatePolicy(p, role); err != nil {
			return PhasePolicy{}, err
		}
		return PhasePolicy{Runtime: "copilot", Executable: p.Executable, Role: p.Role, Agent: p.Agent, SourceProfile: p.SourceProfile, Copilot: &p}, nil
	case 2:
		var p policyV2
		if err := decodeStrict(data, &p); err != nil {
			return PhasePolicy{}, err
		}
		if err := validateV2(p, role); err != nil {
			return PhasePolicy{}, err
		}
		return PhasePolicy{Runtime: p.Runtime, Executable: p.Executable, Role: p.Role, Agent: p.Agent, SourceProfile: p.SourceProfile, Grants: p.Grants, Model: p.Model}, nil
	default:
		return PhasePolicy{}, fmt.Errorf("unsupported policy version %d", probe.Version)
	}
}

func validateV2(p policyV2, role string) error {
	if p.Runtime != "copilot" && p.Runtime != "claude" {
		return fmt.Errorf("unsupported runtime %q", p.Runtime)
	}
	if p.Role != role || !safeName(p.Agent) || !filepath.IsAbs(p.Executable) || !filepath.IsAbs(p.SourceProfile) {
		return errors.New("policy requires matching role, safe agent name and absolute executable/profile paths")
	}
	switch role {
	case "producer", "specialist", "dev", "qa", "review":
	default:
		return errors.New("unknown adapter role")
	}
	if p.Model != "" && !modelPattern.MatchString(p.Model) {
		return errors.New("model must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	if p.Model != "" && p.Runtime == "copilot" {
		return errors.New("the copilot runtime does not support model selection")
	}
	seen := map[string]bool{}
	for _, g := range p.Grants {
		if seen[g] {
			return errors.New("duplicate grant")
		}
		seen[g] = true
		switch g {
		case "read":
		case "write":
			if role != "dev" {
				return errors.New("nonwriting role cannot be granted write")
			}
		default:
			return fmt.Errorf("unsupported grant %q; shell/blanket grants are not supported", g)
		}
	}
	if !seen["read"] {
		return errors.New("grants must include read (empty grants grant nothing)")
	}
	return nil
}
