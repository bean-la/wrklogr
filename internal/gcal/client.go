package gcal

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const timeLayout = "2006-01-02T15:04:05"

type Event struct {
	Title  string
	Start  time.Time
	End    time.Time
	Length time.Duration
}

func FetchEvents(gcalcliPath, calendar string, since, until time.Time) ([]Event, error) {
	cmdPath := gcalcliPath
	if cmdPath == "" {
		var err error
		cmdPath, err = exec.LookPath("gcalcli")
		if err != nil {
			return nil, fmt.Errorf("gcalcli not found in PATH; install with: pip3 install gcalcli")
		}
	}

	sinceStr := since.Format("2006-01-02")
	untilStr := until.Format("2006-01-02")

	args := []string{
		"--calendar", calendar,
		"--tsv",
		"--details", "title,time,length",
		"agenda", sinceStr, untilStr,
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(cmdPath, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return nil, fmt.Errorf("gcalcli failed: %s", stderrStr)
		}
		return nil, fmt.Errorf("gcalcli failed: %w", err)
	}

	return parseTSV(stdout.String())
}

func parseTSV(raw string) ([]Event, error) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	events := make([]Event, 0, len(lines))

	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) < 4 {
			continue
		}

		title := strings.TrimSpace(fields[0])
		startStr := strings.TrimSpace(fields[1])
		endStr := strings.TrimSpace(fields[2])
		lenStr := strings.TrimSpace(fields[3])

		if title == "" || startStr == "" {
			continue
		}

		start, err := time.Parse(timeLayout, startStr)
		if err != nil {
			continue
		}

		var end time.Time
		if endStr != "" {
			end, err = time.Parse(timeLayout, endStr)
			if err != nil {
				end = start
			}
		} else {
			end = start
		}

		var length time.Duration
		if lenStr != "" {
			minutes, err := strconv.Atoi(lenStr)
			if err == nil {
				length = time.Duration(minutes) * time.Minute
			}
		}
		if length == 0 {
			length = end.Sub(start)
		}

		events = append(events, Event{
			Title:  title,
			Start:  start,
			End:    end,
			Length: length,
		})
	}

	return events, nil
}
