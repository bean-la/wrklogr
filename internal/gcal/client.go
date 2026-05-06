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

func FetchEvents(calendarID string, since, until time.Time) ([]Event, error) {
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

	return parseICS(string(raw), since, until)
}

func parseICS(raw string, since, until time.Time) ([]Event, error) {
	events := make([]Event, 0)

	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")

	var inEvent bool
	var title string
	var dtStart, dtEnd string

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
			continue
		}

		if line == "END:VEVENT" {
			inEvent = false
			if title == "" || dtStart == "" {
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

func unescapeICalText(s string) string {
	s = strings.ReplaceAll(s, `\,`, ",")
	s = strings.ReplaceAll(s, `\;`, ";")
	s = strings.ReplaceAll(s, `\\`, "\\")
	s = strings.ReplaceAll(s, `\n`, " ")
	s = strings.ReplaceAll(s, `\N`, " ")
	return s
}
