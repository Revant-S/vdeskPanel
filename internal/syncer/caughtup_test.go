package syncer_test

import (
	"testing"

	"vault/internal/devicetest"
)

func TestCaughtUpOnlyAfterAFullPoll(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a := n.NewDevice(t, "a")
	if !a.Incoming.CaughtUpAt().IsZero() {
		t.Fatal("caught up before any poll")
	}
	a.Poll()
	if a.Incoming.CaughtUpAt().IsZero() {
		t.Fatal("not caught up after a successful poll")
	}
}
