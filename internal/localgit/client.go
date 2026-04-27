package localgit

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const fieldSep = "\x1f"

type Commit struct {
	SHA            string
	AuthorDate     time.Time
	CommitterDate  time.Time
	AuthorName     string
	AuthorEmail    string
	CommitterEmail string
	Subject        string
}

func ListCommits(repoPath string, since *time.Time, until *time.Time) ([]Commit, error) {
	args := []string{
		"-C", repoPath,
		"log",
		"--pretty=format:%H%x1f%aI%x1f%cI%x1f%an%x1f%ae%x1f%ce%x1f%s",
	}
	if since != nil {
		args = append(args, "--since="+since.Format(time.RFC3339))
	}
	if until != nil {
		args = append(args, "--until="+until.Format(time.RFC3339))
	}

	cmd := exec.Command("git", args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git log for %s: %w (%s)", repoPath, err, strings.TrimSpace(stderr.String()))
	}

	raw := strings.TrimSpace(stdout.String())
	if raw == "" {
		return nil, nil
	}

	lines := strings.Split(raw, "\n")
	out := make([]Commit, 0, len(lines))
	for _, line := range lines {
		parts := strings.Split(line, fieldSep)
		if len(parts) < 7 {
			continue
		}
		authorDate, err := time.Parse(time.RFC3339, parts[1])
		if err != nil {
			continue
		}
		committerDate, err := time.Parse(time.RFC3339, parts[2])
		if err != nil {
			continue
		}
		out = append(out, Commit{
			SHA:            strings.TrimSpace(parts[0]),
			AuthorDate:     authorDate,
			CommitterDate:  committerDate,
			AuthorName:     strings.TrimSpace(parts[3]),
			AuthorEmail:    strings.ToLower(strings.TrimSpace(parts[4])),
			CommitterEmail: strings.ToLower(strings.TrimSpace(parts[5])),
			Subject:        strings.TrimSpace(parts[6]),
		})
	}
	return out, nil
}

func CurrentEmails(repoPath string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, args := range [][]string{
		{"-C", repoPath, "config", "--get", "user.email"},
		{"config", "--global", "--get", "user.email"},
	} {
		cmd := exec.Command("git", args...)
		raw, err := cmd.Output()
		if err != nil {
			continue
		}
		email := strings.ToLower(strings.TrimSpace(string(raw)))
		if email != "" {
			out[email] = struct{}{}
		}
	}
	return out
}
