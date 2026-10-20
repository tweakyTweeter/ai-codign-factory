package cli

import (
	"context"
	"errors"
	"flag"
	"io"

	"github.com/anskumar/ai-coding-factory/internal/factory"
)

func adapter(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	policy, report, fingerprint := new(string), new(string), new(string)
	if name == "copilot" {
		fs.StringVar(policy, "policy", "", "explicit Copilot tool/profile policy JSON")
	} else {
		fs.StringVar(report, "report", "", "canonical roborev schema v2 JSON report (native execution unsupported)")
		fs.StringVar(fingerprint, "fingerprint", "", "explicit fingerprint actually reviewed by this report")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return &UsageError{err}
	}
	if fs.NArg() != 0 {
		return &UsageError{errors.New("unexpected adapter arguments")}
	}
	var err error
	if name == "copilot" {
		if *policy == "" {
			return &UsageError{errors.New("--policy is required")}
		}
		err = factory.Copilot(ctx, *policy)
	} else {
		if *report == "" || *fingerprint == "" {
			return &UsageError{errors.New("--report and --fingerprint are required")}
		}
		err = factory.RoborevImport(*report, *fingerprint)
	}
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, "{\"adapter\":\""+name+"\",\"completed\":true}\n")
	return err
}
