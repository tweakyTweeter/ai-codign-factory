package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeDecisionBeads struct {
	t   *testing.T
	dir string
}

func newFakeDecisionBeads(t *testing.T) (fakeDecisionBeads, []string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "bd")
	body := "#!/bin/sh\necho \"$*\" >> '" + dir + "/calls'\ncase \"$1\" in\n" +
		"show) cat '" + dir + "'/\"$2\".show ;;\n" +
		"comments) cat '" + dir + "'/\"$2\".comments ;;\n" +
		"*) exit 9 ;;\nesac\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return fakeDecisionBeads{t: t, dir: dir}, []string{script}
}

func (f fakeDecisionBeads) set(id, show, comments string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, id+".show"), []byte(show), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, id+".comments"), []byte(comments), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f fakeDecisionBeads) calls() string {
	b, _ := os.ReadFile(filepath.Join(f.dir, "calls"))
	return string(b)
}

const (
	pendingShow   = `[{"id":"d-1","title":"Pick store","status":"open","labels":["factory-intake","human"],"comment_count":0}]`
	answeredShow  = `[{"id":"d-1","title":"Pick store","status":"open","labels":["factory-intake"],"comment_count":1}]`
	answeredNotes = `[{"id":"c1","issue_id":"d-1","author":"Ana","text":"Use SQLite","created_at":"2026-10-21T00:00:00Z"}]`
)

func TestAwaitDecisions_ResumesAfterScottyCommentRemovesHumanLabel(t *testing.T) {
	fake, argv := newFakeDecisionBeads(t)
	fake.set("d-1", pendingShow, "[]")
	done := make(chan struct{})
	var got []Decision
	var err error
	go func() {
		got, err = AwaitDecisions(context.Background(), Repo{Root: fake.dir}, argv, []string{"d-1", "d-1"}, time.Second, 10*time.Second)
		close(done)
	}()
	time.Sleep(300 * time.Millisecond)
	fake.set("d-1", answeredShow, answeredNotes)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].State != DecisionAnswered || got[0].Title != "Pick store" || len(got[0].Answers) != 1 || got[0].Answers[0].Text != "Use SQLite" || got[0].Answers[0].Author != "Ana" {
		t.Fatalf("got %+v", got)
	}
	calls := fake.calls()
	if !strings.Contains(calls, "show d-1 --json --sandbox") || !strings.Contains(calls, "comments d-1 --json --sandbox") {
		t.Fatalf("calls %q", calls)
	}
	for _, write := range []string{"update", "close", "label", "comments add"} {
		if strings.Contains(calls, write) {
			t.Fatalf("waiter must only read Beads, saw %q in %q", write, calls)
		}
	}
}

func TestAwaitDecisions_Outcomes(t *testing.T) {
	tests := []struct {
		name, show, comments string
		state                string
		wantErr              error
	}{
		{"closed with response", `[{"id":"d-1","title":"Q","status":"closed","close_reason":"Responded","labels":["human"],"comment_count":1}]`, answeredNotes, DecisionAnswered, nil},
		{"dismissed", `[{"id":"d-1","title":"Q","status":"closed","close_reason":"Dismissed","labels":["human"],"comment_count":0}]`, "[]", DecisionDismissed, ErrDecisionUnanswered},
		{"label removed without comment", `[{"id":"d-1","title":"Q","status":"open","labels":[],"comment_count":0}]`, "[]", DecisionCleared, ErrDecisionUnanswered},
		{"only blank comments", answeredShow, `[{"id":"c1","issue_id":"d-1","author":"x","text":"  "}]`, DecisionCleared, ErrDecisionUnanswered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake, argv := newFakeDecisionBeads(t)
			fake.set("d-1", tt.show, tt.comments)
			got, err := AwaitDecisions(context.Background(), Repo{Root: fake.dir}, argv, []string{"d-1"}, time.Second, 0)
			if !errors.Is(err, tt.wantErr) && !(tt.wantErr == nil && err == nil) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if len(got) != 1 || got[0].State != tt.state {
				t.Fatalf("got %+v, want state %s", got, tt.state)
			}
		})
	}
}

func TestAwaitDecisions_TimeoutLeavesPending(t *testing.T) {
	fake, argv := newFakeDecisionBeads(t)
	fake.set("d-1", pendingShow, "[]")
	got, err := AwaitDecisions(context.Background(), Repo{Root: fake.dir}, argv, []string{"d-1"}, time.Second, 200*time.Millisecond)
	if !errors.Is(err, ErrDecisionTimeout) || len(got) != 1 || got[0].State != DecisionPending {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestAwaitDecisions_RejectsBadInputAndForeignIssue(t *testing.T) {
	fake, argv := newFakeDecisionBeads(t)
	repo := Repo{Root: fake.dir}
	for name, ids := range map[string][]string{"empty": nil, "blank": {" "}, "flag": {"--all"}} {
		if _, err := AwaitDecisions(context.Background(), repo, argv, ids, time.Second, 0); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
	if _, err := AwaitDecisions(context.Background(), repo, argv, []string{"d-1"}, time.Millisecond, 0); err == nil {
		t.Fatal("expected poll interval error")
	}
	fake.set("d-1", `[{"id":"other","status":"open","labels":["human"]}]`, "[]")
	if _, err := AwaitDecisions(context.Background(), repo, argv, []string{"d-1"}, time.Second, 0); err == nil || !strings.Contains(err.Error(), "different") {
		t.Fatalf("expected foreign issue error, got %v", err)
	}
}
