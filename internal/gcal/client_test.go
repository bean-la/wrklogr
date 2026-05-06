package gcal

import (
	"testing"
	"time"
)

func TestParseTSV(t *testing.T) {
	t.Parallel()

	raw := "Team standup\t2026-04-25T09:00:00\t2026-04-25T09:30:00\t30\n" +
		"Client call\t2026-04-25T14:00:00\t2026-04-25T14:45:00\t45\n"

	events, err := parseTSV(raw)
	if err != nil {
		t.Fatalf("parseTSV returned error: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	if events[0].Title != "Team standup" {
		t.Fatalf("expected title 'Team standup', got %q", events[0].Title)
	}
	if events[0].Length != 30*time.Minute {
		t.Fatalf("expected length 30m, got %v", events[0].Length)
	}
	if events[1].Length != 45*time.Minute {
		t.Fatalf("expected length 45m, got %v", events[1].Length)
	}
}

func TestParseTSVEmpty(t *testing.T) {
	t.Parallel()

	events, err := parseTSV("")
	if err != nil {
		t.Fatalf("parseTSV returned error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}

func TestParseTSVSkipInvalid(t *testing.T) {
	t.Parallel()

	raw := "Valid\t2026-04-25T10:00:00\t2026-04-25T11:00:00\t60\n" +
		"BadDate\tnot-a-date\t\t\n" +
		"\t2026-04-25T12:00:00\t\t\n"

	events, err := parseTSV(raw)
	if err != nil {
		t.Fatalf("parseTSV returned error: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 valid event, got %d", len(events))
	}
	if events[0].Title != "Valid" {
		t.Fatalf("expected title 'Valid', got %q", events[0].Title)
	}
}
