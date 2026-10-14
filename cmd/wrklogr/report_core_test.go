package main

import (
	"testing"
	"time"

	"github.com/bean-la/wrklogr/internal/session"
)

func TestUncoveredCalendarMinutes(t *testing.T) {
	base := time.Date(2026, time.October, 2, 13, 0, 0, 0, time.UTC)
	sessions := []session.Session{{Start: base, End: base.Add(30 * time.Minute)}}
	if got := uncoveredCalendarMinutes(base, base.Add(time.Hour), sessions); got != 30 {
		t.Fatalf("uncoveredCalendarMinutes() = %d, want 30", got)
	}
}
