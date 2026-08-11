package gcal

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const icalURL = "https://calendar.google.com/calendar/ical/%s/public/basic.ics"

type Event struct {
	Title  string
	Start  time.Time
	End    time.Time
	Length time.Duration
}

func FetchEvents(calendarID string, since, until time.Time, requiredAttendee string, keywords []string) ([]Event, error) {
	if calendarID == "" {
		return nil, fmt.Errorf("calendar ID is required")
	}

	icalURL := fmt.Sprintf(icalURL, url.QueryEscape(calendarID))

	resp, err := http.Get(icalURL)
	if err != nil {
		return nil, fmt.Errorf("fetch iCal feed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("iCal feed returned HTTP %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read iCal feed: %w", err)
	}

	return parseICS(string(raw), since, until, requiredAttendee, keywords)
}

func parseICS(raw string, since, until time.Time, requiredAttendee string, keywords []string) ([]Event, error) {
	events := make([]Event, 0)

	unfolded := unfoldICSLines(raw)
	lines := strings.Split(unfolded, "\n")

	var inEvent bool
	var title string
	var dtStart, dtEnd string
	var hasAttendee bool

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if line == "BEGIN:VEVENT" {
			inEvent = true
			title = ""
			dtStart = ""
			dtEnd = ""
			hasAttendee = false
			continue
		}

		if line == "END:VEVENT" {
			inEvent = false
			if title == "" || dtStart == "" {
				continue
			}
			if requiredAttendee != "" && !hasAttendee {
				continue
			}
			if len(keywords) > 0 && !matchesAnyKeyword(title, keywords) {
				continue
			}

			start, err := parseICalTime(dtStart)
			if err != nil {
				continue
			}

			end := start
			if dtEnd != "" {
				parsedEnd, err := parseICalTime(dtEnd)
				if err == nil {
					end = parsedEnd
				}
			}

			if start.After(until) || end.Before(since) {
				continue
			}

			length := end.Sub(start)
			if length < 0 {
				length = 0
			}

			events = append(events, Event{
				Title:  title,
				Start:  start,
				End:    end,
				Length: length,
			})
			continue
		}

		if !inEvent {
			continue
		}

		if strings.HasPrefix(line, "SUMMARY:") {
			title = strings.TrimPrefix(line, "SUMMARY:")
			title = unescapeICalText(title)
			continue
		}

		if strings.HasPrefix(line, "DTSTART") {
			dtStart = extractICalValue(line)
			continue
		}

		if strings.HasPrefix(line, "DTEND") {
			dtEnd = extractICalValue(line)
			continue
		}

		if requiredAttendee != "" && strings.HasPrefix(line, "ATTENDEE") {
			if strings.Contains(strings.ToLower(line), strings.ToLower(requiredAttendee)) {
				hasAttendee = true
			}
			continue
		}
	}

	return events, nil
}

func extractICalValue(line string) string {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return ""
	}
	return line[idx+1:]
}

func parseICalTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)

	// UTC format: 20260425T090000Z
	if strings.HasSuffix(s, "Z") {
		s = strings.TrimSuffix(s, "Z")
		if len(s) >= 15 {
			return time.Parse("20060102T150405", s)
		}
		return time.Parse("20060102", s)
	}

	// Local time (no TZID): 20260425T090000
	if len(s) >= 15 {
		return time.Parse("20060102T150405", s)
	}

	// Date only: 20260425
	return time.Parse("20060102", s)
}

// unfoldICSLines joins RFC 5545 line continuations.  ICS lines are folded by
// inserting CRLF followed by a space or tab after column 75.  This function
// normalises CRLF→LF, then joins continuation lines so each logical line
// appears on a single physical line.
func unfoldICSLines(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		// If this line starts with a space or tab, append it to the previous line
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if len(out) > 0 {
				out[len(out)-1] += line[1:]
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func matchesAnyKeyword(title string, keywords []string) bool {
	titleLower := strings.ToLower(title)
	for _, kw := range keywords {
		if strings.Contains(titleLower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

func unescapeICalText(s string) string {
	s = strings.ReplaceAll(s, `\,`, ",")
	s = strings.ReplaceAll(s, `\;`, ";")
	s = strings.ReplaceAll(s, `\\`, "\\")
	s = strings.ReplaceAll(s, `\n`, " ")
	s = strings.ReplaceAll(s, `\N`, " ")
	return s
}
