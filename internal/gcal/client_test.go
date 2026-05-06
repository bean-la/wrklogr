package gcal

import (
	"testing"
	"time"
)

func TestParseICS(t *testing.T) {
	t.Parallel()

	raw := `BEGIN:VCALENDAR
BEGIN:VEVENT
DTSTART:20260425T090000Z
DTEND:20260425T093000Z
SUMMARY:Team standup
END:VEVENT
BEGIN:VEVENT
DTSTART:20260425T140000Z
DTEND:20260425T144500Z
SUMMARY:Client call
END:VEVENT
END:VCALENDAR`

	since := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 4, 30, 23, 59, 59, 0, time.UTC)

	events, err := parseICS(raw, since, until)
	if err != nil {
		t.Fatalf("parseICS returned error: %v", err)
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

func TestParseICSFiltersByDateRange(t *testing.T) {
	t.Parallel()

	raw := `BEGIN:VCALENDAR
BEGIN:VEVENT
DTSTART:20260301T100000Z
DTEND:20260301T110000Z
SUMMARY:March event
END:VEVENT
BEGIN:VEVENT
DTSTART:20260501T100000Z
DTEND:20260501T110000Z
SUMMARY:May event
END:VEVENT
END:VCALENDAR`

	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 31, 23, 59, 59, 0, time.UTC)

	events, err := parseICS(raw, since, until)
	if err != nil {
		t.Fatalf("parseICS returned error: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Title != "March event" {
		t.Fatalf("expected 'March event', got %q", events[0].Title)
	}
}

func TestParseICSEmpty(t *testing.T) {
	t.Parallel()

	raw := `BEGIN:VCALENDAR
END:VCALENDAR`

	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)

	events, err := parseICS(raw, since, until)
	if err != nil {
		t.Fatalf("parseICS returned error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}

func TestUnescapeICalText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{`Hello\, World`, "Hello, World"},
		{`Foo\;Bar`, "Foo;Bar"},
		{`A\\B`, `A\B`},
		{`Line1\nLine2`, "Line1 Line2"},
		{`Plain text`, "Plain text"},
	}

	for _, tc := range tests {
		got := unescapeICalText(tc.input)
		if got != tc.want {
			t.Fatalf("unescapeICalText(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
