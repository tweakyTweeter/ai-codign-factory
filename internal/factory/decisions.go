package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Decision states reported by AwaitDecisions.
const (
	DecisionAnswered  = "answered"
	DecisionDismissed = "dismissed"
	// DecisionCleared means the human label was removed or the bead closed
	// without any comment, so there is no answer to act on.
	DecisionCleared = "cleared-without-answer"
	DecisionPending = "pending"
)

// HumanLabel puts a bead in Scotty's Needs You view.
const HumanLabel = "human"

const maxDecisions = 50

type DecisionAnswer struct {
	Author    string `json:"author"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}

type Decision struct {
	ID      string           `json:"id"`
	Title   string           `json:"title"`
	State   string           `json:"state"`
	Answers []DecisionAnswer `json:"answers,omitempty"`
}

var (
	ErrDecisionUnanswered = errors.New("decision closed or cleared without an answer")
	ErrDecisionTimeout    = errors.New("timed out waiting for human decisions")
)

// AwaitDecisions blocks until every human-needed decision bead is answered
// through Scotty (comment + human label removed) or bd human respond
// (comment + close). It only reads Beads. A dismissed or answer-less bead
// stops the wait with ErrDecisionUnanswered; timeout 0 waits until ctx ends.
func AwaitDecisions(ctx context.Context, repo Repo, argv []string, ids []string, poll, timeout time.Duration) ([]Decision, error) {
	if err := validateArgv(argv); err != nil {
		return nil, err
	}
	ids, err := decisionIDs(ids)
	if err != nil {
		return nil, err
	}
	if poll < time.Second {
		return nil, errors.New("poll interval must be at least 1s")
	}
	if timeout < 0 {
		return nil, errors.New("timeout must not be negative")
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, timeout, ErrDecisionTimeout)
		defer cancel()
	}
	result := make([]Decision, len(ids))
	for i, id := range ids {
		result[i] = Decision{ID: id, State: DecisionPending}
	}
	for {
		pending, failed := 0, false
		for i := range result {
			if result[i].State != DecisionPending {
				continue
			}
			d, err := readDecision(ctx, repo, argv, result[i].ID)
			if err != nil {
				if ctx.Err() != nil {
					return result, context.Cause(ctx)
				}
				return result, err
			}
			result[i] = d
			switch d.State {
			case DecisionPending:
				pending++
			case DecisionDismissed, DecisionCleared:
				failed = true
			}
		}
		if failed {
			return result, ErrDecisionUnanswered
		}
		if pending == 0 {
			return result, nil
		}
		t := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			t.Stop()
			return result, context.Cause(ctx)
		case <-t.C:
		}
	}
}

func decisionIDs(ids []string) ([]string, error) {
	var out []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || strings.HasPrefix(id, "-") || len(id) > 256 {
			return nil, fmt.Errorf("invalid decision ID %q", id)
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	if len(out) == 0 || len(out) > maxDecisions {
		return nil, fmt.Errorf("between 1 and %d decision IDs required", maxDecisions)
	}
	return out, nil
}

func readDecision(ctx context.Context, repo Repo, argv []string, id string) (Decision, error) {
	b, err := beadCommand(ctx, repo, argv, "show", id, "--json")
	if err != nil {
		return Decision{}, fmt.Errorf("beads show %s: %w", id, err)
	}
	var shown []struct {
		ID           string   `json:"id"`
		Title        string   `json:"title"`
		Status       string   `json:"status"`
		CloseReason  string   `json:"close_reason"`
		Labels       []string `json:"labels"`
		CommentCount int      `json:"comment_count"`
	}
	if err := json.Unmarshal(b, &shown); err != nil {
		return Decision{}, fmt.Errorf("beads show %s JSON: %w", id, err)
	}
	if len(shown) != 1 || shown[0].ID != id {
		return Decision{}, fmt.Errorf("beads show %s returned a different or ambiguous issue", id)
	}
	s := shown[0]
	d := Decision{ID: id, Title: s.Title}
	closed := s.Status == "closed"
	switch {
	case closed && s.CloseReason == "Dismissed":
		d.State = DecisionDismissed
		return d, nil
	case !closed && slices.Contains(s.Labels, HumanLabel):
		d.State = DecisionPending
		return d, nil
	case s.CommentCount == 0:
		d.State = DecisionCleared
		return d, nil
	}
	b, err = beadCommand(ctx, repo, argv, "comments", id, "--json")
	if err != nil {
		return Decision{}, fmt.Errorf("beads comments %s: %w", id, err)
	}
	var comments []struct {
		IssueID string `json:"issue_id"`
		DecisionAnswer
	}
	if err := json.Unmarshal(b, &comments); err != nil {
		return Decision{}, fmt.Errorf("beads comments %s JSON: %w", id, err)
	}
	for _, c := range comments {
		if c.IssueID == id && strings.TrimSpace(c.Text) != "" {
			d.Answers = append(d.Answers, c.DecisionAnswer)
		}
	}
	if len(d.Answers) == 0 {
		d.State = DecisionCleared
		return d, nil
	}
	d.State = DecisionAnswered
	return d, nil
}
