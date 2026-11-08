package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anskumar/ai-coding-factory/internal/factory"
)

func TestCLIWorker(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" && len(os.Args) > i+1 {
			switch os.Args[i+1] {
			case "worker":
				if os.Getenv("FACTORY_CONTEXT_FILE") == "" {
					os.Exit(6)
				}
				if err := os.WriteFile("cli-output.txt", []byte("ok"), 0600); err != nil {
					os.Exit(7)
				}
			case "check":
				if b, err := os.ReadFile("cli-output.txt"); err != nil || string(b) != "ok" {
					os.Exit(8)
				}
			default:
				os.Exit(9)
			}
			os.Exit(0)
		}
	}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	git := func(stdin string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = path
		cmd.Stdin = strings.NewReader(stdin)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
			"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git fixture: %v: %s", err, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("", "init", "-q", "--initial-branch=main")
	tree := git("", "mktree")
	head := git("", "commit-tree", tree, "-m", "CLI fixture")
	git("", "update-ref", "refs/heads/main", head)
	return path
}

func invoke(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	err := Run(ctx, args, strings.NewReader("CLI prompt"), &stdout, &stderr)
	return stdout.String(), err
}

func TestRun_CLIEndToEndAndRootResolution(t *testing.T) {
	root := gitRepo(t)
	sub := filepath.Join(root, "subdir")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(t, "init", "--repo", sub); err != nil {
		t.Fatal(err)
	}
	repo, err := factory.Discover(t.Context(), sub)
	if err != nil {
		t.Fatal(err)
	}
	expectedRoot, err := filepath.EvalSymlinks(root)
	if err != nil || repo.Root != expectedRoot {
		t.Fatalf("subdirectory not resolved: %s: %v", repo.Root, err)
	}
	configPath := filepath.Join(repo.State, "config.json")
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(t, "init", "--repo", root); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(original, again) {
		t.Fatal("init overwrote existing config")
	}
	command := func(mode string) factory.Command {
		return factory.Command{Argv: []string{os.Args[0], "-test.run=^TestCLIWorker$", "--", mode}, Timeout: "5s"}
	}
	config := factory.Config{Version: 1, Worker: command("worker"), Checks: []factory.Command{command("check")}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(t, "doctor", "--repo", sub); err != nil {
		t.Fatal(err)
	}
	out, err := invoke(t, "run", "--repo", sub, "--prompt-file", "-")
	if err != nil {
		t.Fatalf("CLI run failed: %s: %v", out, err)
	}
	var r factory.Run
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.State != "awaiting_review" {
		t.Fatalf("CLI output %q: %v", out, err)
	}
	status, err := invoke(t, "status", "--repo", root, "--run", r.ID)
	if err != nil || !strings.Contains(status, `"events"`) || !strings.Contains(status, `"awaiting_review"`) {
		t.Fatalf("CLI status %q: %v", status, err)
	}
	if _, err := invoke(t, "run", "--repo", root, "--prompt-file", "-"); err == nil {
		t.Fatal("duplicate CLI run admitted")
	}
	out, err = invoke(t, "review", "--repo", root, "--run", r.ID)
	var reviewed factory.Run
	if err != nil || json.Unmarshal([]byte(out), &reviewed) != nil || reviewed.HumanReview == nil ||
		reviewed.HumanReview.PublishState != "unpublished" || reviewed.HumanReview.Decision != "not-approved" {
		t.Fatalf("review bundle CLI output %q: %v", out, err)
	}
	for _, command := range []string{"review-publish", "review-sync", "integrate"} {
		out, err = invoke(t, command, "--repo", root, "--run", r.ID)
		if err == nil || json.Unmarshal([]byte(out), &reviewed) != nil || reviewed.HumanReview.Decision != "not-approved" {
			t.Fatalf("generic run %s granted approval or lost JSON output: %q: %v", command, out, err)
		}
	}
	if _, err := invoke(t, "cancel", "--repo", root, "--run", r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(t, "reconcile", "--repo", root); err != nil {
		t.Fatal(err)
	}
	out, err = invoke(t, "release", "--repo", root, "--run", r.ID, "--reason", "human acknowledged")
	if err != nil || !strings.Contains(out, `"released"`) {
		t.Fatalf("release %q: %v", out, err)
	}
}

func TestRun_HelpAndUsageErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"run", "--help"}, {"review", "--help"}, {"review-publish", "--help"}, {"review-sync", "--help"}, {"integrate", "--help"}} {
		if _, err := invoke(t, args...); err != nil {
			t.Fatalf("help %v: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"bogus"}, {"run"}, {"run", "--unknown"}, {"status", "unexpected"},
		{"cancel"}, {"release", "--run", "x"}, {"release"},
		{"review"}, {"review-publish"}, {"review-sync"}, {"integrate"},
		{"decisions"}, {"decisions", "wait"}, {"decisions", "await"}, {"decisions", "await", "--ids", "a", "extra"},
	} {
		_, err := invoke(t, args...)
		var usage *UsageError
		if !errors.As(err, &usage) {
			t.Fatalf("expected usage error for %v, got %v", args, err)
		}
	}
}

func TestAgentCommand_RequiresPolicy(t *testing.T) {
	_, err := invoke(t, "agent")
	var usage *UsageError
	if !errors.As(err, &usage) {
		t.Fatalf("agent without --policy: want usage error, got %v", err)
	}
}

func TestAgentCommand_RejectsPositionalArgs(t *testing.T) {
	_, err := invoke(t, "agent", "--policy", "/nope", "extra")
	var usage *UsageError
	if !errors.As(err, &usage) {
		t.Fatalf("extra positional: want usage error, got %v", err)
	}
}

// doctorWorkflow writes a workflow config whose phases are copilot-policy
// driven. policies maps phase role to the role written into its policy file;
// extra phases may be added by the caller via specialist.
func doctorWorkflow(t *testing.T, qaPolicyRole string, specialist bool) string {
	t.Helper()
	root := gitRepo(t)
	if _, err := invoke(t, "init", "--repo", root); err != nil {
		t.Fatal(err)
	}
	repo, err := factory.Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "p.md")
	if err := os.WriteFile(profile, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	phase := func(name, role, policyRole string, tools, allow string) factory.Phase {
		pol := filepath.Join(root, name+"-policy.json")
		body := `{"version":1,"executable":"` + os.Args[0] + `","role":"` + policyRole + `","agent":"a","source_profile":"` + profile + `","available_tools":` + tools + `,"allow_tools":` + allow + `}`
		if err := os.WriteFile(pol, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return factory.Phase{Name: name, Role: role, Command: factory.Command{Argv: []string{os.Args[0], "copilot", "-policy=" + pol}, Timeout: "5s"}}
	}
	ro := `["view","rg","glob"]`
	phases := []factory.Phase{phase("producer", "producer", "producer", ro, "[]")}
	if specialist {
		mine := phase("mine", "specialist", "specialist", ro, "[]")
		mine.Profile = "mine"
		other := phase("other", "specialist", "specialist", ro, "[]")
		other.Profile = "other"
		other.Command.Argv = []string{os.Args[0], "copilot", "--policy", filepath.Join(root, "missing.json")}
		phases = append(phases, mine, other)
	}
	phases = append(phases,
		phase("dev", "dev", "dev", `["view","rg","glob","create","edit","apply_patch"]`, `["write"]`),
		phase("qa", "qa", qaPolicyRole, ro, "[]"),
		phase("review", "review", "review", ro, "[]"))
	data, err := json.Marshal(factory.Config{Version: 1, BeadsArgv: []string{os.Args[0]}, Checks: []factory.Command{{Argv: []string{os.Args[0]}, Timeout: "5s"}}, Workflow: &factory.Workflow{Phases: phases}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.State, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDoctor_PhaseRuntimesForCopilotPolicies(t *testing.T) {
	root := doctorWorkflow(t, "qa", false)
	out, err := invoke(t, "doctor", "--repo", root)
	if err != nil {
		t.Fatalf("doctor: %s: %v", out, err)
	}
	var got struct {
		PhaseRuntimes []factory.PhaseRuntime `json:"phase_runtimes"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.PhaseRuntimes) != 4 || got.PhaseRuntimes[0].Runtime != "copilot" {
		t.Fatalf("phase_runtimes: %s: %v", out, err)
	}
}

func TestDoctor_InvalidPolicyFails(t *testing.T) {
	root := doctorWorkflow(t, "dev", false)
	if out, err := invoke(t, "doctor", "--repo", root); err == nil {
		t.Fatalf("doctor accepted a qa phase with a dev policy: %s", out)
	}
}

func TestAdapters_RejectRelativePolicy(t *testing.T) {
	for _, name := range []string{"agent", "copilot"} {
		for _, rel := range []string{"policy.json", "./policy.json"} {
			_, err := invoke(t, name, "--policy", rel)
			var usage *UsageError
			if err == nil || errors.As(err, &usage) || !strings.Contains(err.Error(), "failure_reason=policy") || !strings.Contains(err.Error(), "absolute") || !strings.Contains(err.Error(), rel) {
				t.Fatalf("%s --policy %s: want failure_reason=policy absolute-path error, got %v", name, rel, err)
			}
		}
	}
}

func TestDoctor_RelativePolicyFails(t *testing.T) {
	for _, rel := range []string{"qa-policy.json", "./qa-policy.json"} {
		t.Run(rel, func(t *testing.T) {
			root := doctorWorkflow(t, "qa", false)
			// A valid qa-policy.json exists at root; resolving the relative
			// path there would succeed, so a failure proves it is refused.
			t.Chdir(root)
			repo, err := factory.Discover(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			cfgPath := filepath.Join(repo.State, "config.json")
			data, err := os.ReadFile(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			var cfg factory.Config
			if err := json.Unmarshal(data, &cfg); err != nil {
				t.Fatal(err)
			}
			for i, p := range cfg.Workflow.Phases {
				if p.Name == "qa" {
					cfg.Workflow.Phases[i].Command.Argv = []string{os.Args[0], "agent", "--policy", rel}
				}
			}
			if data, err = json.Marshal(cfg); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfgPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			out, err := invoke(t, "doctor", "--repo", root)
			if err == nil {
				t.Fatalf("doctor accepted relative --policy %s: %s", rel, out)
			}
			if !strings.Contains(err.Error()+out, "absolute") {
				t.Fatalf("doctor error does not name the absolute-path rule: %s: %v", out, err)
			}
		})
	}
}

func TestDoctor_ProfileSkipsOtherSpecialistPolicies(t *testing.T) {
	root := doctorWorkflow(t, "qa", true)
	if out, err := invoke(t, "doctor", "--repo", root, "--profile", "other"); err == nil {
		t.Fatalf("selected specialist with a missing policy accepted: %s", out)
	}
	if out, err := invoke(t, "doctor", "--repo", root, "--profile", "mine"); err != nil {
		t.Fatalf("other profile's policy inspected: %s: %v", out, err)
	}
}

func TestHelpListsAgent(t *testing.T) {
	out, err := invoke(t, "help")
	if err != nil || !strings.Contains(out, "copilot, agent,") {
		t.Fatalf("help: %q: %v", out, err)
	}
}
