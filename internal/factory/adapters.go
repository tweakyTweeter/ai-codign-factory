package factory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CopilotPolicy is explicit approval, not a sandbox or an arbitrary flag escape
// hatch. Shell, MCP, task and extension tools are deliberately not supported.
type CopilotPolicy struct {
	Version        int      `json:"version"`
	Executable     string   `json:"executable"`
	Role           string   `json:"role"`
	Agent          string   `json:"agent"`
	SourceProfile  string   `json:"source_profile"`
	AvailableTools []string `json:"available_tools"`
	AllowTools     []string `json:"allow_tools"`
}

func validatePolicy(p CopilotPolicy, role string) error {
	if p.Version != 1 || p.Role != role || !safeName(p.Agent) || !filepath.IsAbs(p.Executable) || !filepath.IsAbs(p.SourceProfile) {
		return errors.New("policy requires version 1, matching role, safe agent name and absolute executable/profile paths")
	}
	switch role {
	case "producer", "specialist", "dev", "qa", "review":
	default:
		return errors.New("unknown adapter role")
	}
	if len(p.AvailableTools) == 0 || p.AllowTools == nil {
		return errors.New("explicit available_tools and allow_tools are required (empty approvals grant nothing)")
	}
	approvedWrite := false
	for _, approval := range p.AllowTools {
		switch approval {
		case "read":
		case "write":
			if role != "dev" {
				return errors.New("nonwriting role cannot approve write")
			}
			approvedWrite = true
		default:
			return fmt.Errorf("unsupported tool approval %q; shell/blanket approval is not supported", approval)
		}
	}
	seen := map[string]bool{}
	for _, tool := range p.AvailableTools {
		if seen[tool] {
			return errors.New("duplicate available tool")
		}
		seen[tool] = true
		switch tool {
		case "view", "rg", "glob":
		case "create", "edit", "apply_patch":
			if role != "dev" || !approvedWrite {
				return errors.New("create/edit require explicit Dev write approval")
			}
		default:
			return fmt.Errorf("unsupported available tool %q", tool)
		}
	}
	return nil
}

const copilotLaunchSettings = "{\"disableAllHooks\":true}\n"

// Copilot runs the standalone 1.0.100 headless contract through the shared
// phase runner. Global extensions may still load; filtering visible tools and
// explicit permissions is not OS isolation.
func Copilot(ctx context.Context, policyPath string) error {
	return RunPhase(ctx, policyPath, "copilot")
}

// privateLaunchDir creates a 0700 temp directory and fails closed if any
// ancestor is a git checkout, which could contribute repo settings or hooks.
func privateLaunchDir(prefix string) (string, error) {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", errors.Join(err, os.RemoveAll(dir))
	}
	for p := filepath.Dir(resolved); ; p = filepath.Dir(p) {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
			return "", errors.Join(fmt.Errorf("private launch dir %s is inside git checkout %s", resolved, p), os.RemoveAll(dir))
		}
		if filepath.Dir(p) == p {
			return resolved, nil
		}
	}
}

func writeExclusive(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	return errors.Join(err, f.Sync(), f.Close())
}

func copilotFinal(raw []byte) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), outputLimit)
	content, final, terminal := "", false, false
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		if terminal {
			return "", errors.New("Copilot emitted data after terminal result")
		}
		var event struct {
			Type string `json:"type"`
			Data struct {
				Phase   string `json:"phase"`
				Content string `json:"content"`
			} `json:"data"`
			ExitCode *int `json:"exitCode"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return "", fmt.Errorf("Copilot JSONL: %w", err)
		}
		switch event.Type {
		case "error", "session.error":
			return "", errors.New("Copilot stream reported an error")
		case "assistant.message":
			if event.Data.Phase == "final_answer" {
				if final {
					return "", errors.New("multiple Copilot final answers")
				}
				final, content = true, event.Data.Content
			}
		case "result":
			if event.ExitCode == nil || *event.ExitCode != 0 || !final || strings.TrimSpace(content) == "" {
				return "", errors.New("Copilot missing final answer or unsuccessful result")
			}
			terminal = true
		case "":
			return "", errors.New("Copilot event missing type")
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if !terminal {
		return "", errors.New("Copilot stream missing successful terminal result")
	}
	return content, nil
}

// RoborevImport ingests canonical structuredreview schema v2 as defined by
// roborev v0.71.0 pkg/structuredreview/document.go. Native roborev execution is
// deliberately unsupported: its Copilot runner adds --allow-all-tools.
// The caller must explicitly attest the fingerprint the report reviewed;
// importing arbitrary old reports is not proof of a fresh independent review.
func RoborevImport(report, reviewedFingerprint string) error {
	expected, result := os.Getenv("FACTORY_PATCH_FINGERPRINT"), os.Getenv("FACTORY_RESULT_FILE")
	if len(expected) != 64 || expected != reviewedFingerprint || !filepath.IsAbs(result) {
		return errors.New("explicit reviewed fingerprint must match controller phase")
	}
	b, err := readBounded(report)
	if err != nil {
		return err
	}
	var doc struct {
		Version  int    `json:"schema_version"`
		Verdict  string `json:"verdict"`
		Summary  string `json:"summary"`
		Findings []struct {
			Severity string          `json:"severity"`
			Problem  string          `json:"problem"`
			Fix      string          `json:"fix"`
			Location json.RawMessage `json:"location"`
		} `json:"findings"`
	}
	if err := decodeStrict(b, &doc); err != nil {
		return err
	}
	if doc.Version != 2 || strings.TrimSpace(doc.Summary) == "" || doc.Findings == nil {
		return errors.New("invalid roborev schema v2")
	}
	v := Verdict{Version: 1, Summary: doc.Summary, Fingerprint: expected, Findings: []Finding{}}
	switch doc.Verdict {
	case "pass", "fail":
		v.Verdict = doc.Verdict
	case "unable_to_review":
		v.Verdict = "inconclusive"
	default:
		return errors.New("unknown roborev verdict")
	}
	for _, f := range doc.Findings {
		if len(f.Location) == 0 {
			return errors.New("roborev finding requires explicit location (string or null)")
		}
		if !bytes.Equal(f.Location, []byte("null")) {
			var location string
			if err := json.Unmarshal(f.Location, &location); err != nil {
				return errors.New("invalid roborev location")
			}
		}
		switch f.Severity {
		case "critical", "high", "medium", "low":
		default:
			return errors.New("unknown roborev severity")
		}
		if strings.TrimSpace(f.Problem) == "" || strings.TrimSpace(f.Fix) == "" {
			return errors.New("roborev finding requires problem and fix")
		}
		v.Findings = append(v.Findings, Finding{Severity: "blocking", Summary: f.Problem + "; fix: " + f.Fix})
	}
	normalized, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	output := os.Getenv("FACTORY_OUTPUT_DIR")
	if result != filepath.Join(output, "result.json") {
		return errors.New("invalid phase result path")
	}
	if err := writeExclusive(filepath.Join(output, "roborev-source.json"), b); err != nil {
		return err
	}
	if err := writeExclusive(result, normalized); err != nil {
		return err
	}
	_, err = readVerdict(result, expected)
	return err
}
