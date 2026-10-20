package factory

import (
	"context"
	"errors"
	"testing"
)

func TestStore_HumanGateCannotOvertakeDurableCancellation(t *testing.T) {
	c := setupController(t)
	r := Run{ID: "gate-cancellation", Evidence: []Evidence{}}
	if err := c.Store.Save(t.Context(), &r, "checks_passed", "fixture", true); err != nil {
		t.Fatal(err)
	}
	if err := c.Store.RequestCancel(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Store.Save(t.Context(), &r, "awaiting_review", "unsafe gate", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("gate overtook cancellation: %v", err)
	}
	requireState(t, c, r.ID, "checks_passed")
	events, err := c.Store.Events(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.State == "awaiting_review" {
			t.Fatal("cancelled run journaled as successful")
		}
	}
}
