package main

import (
	"testing"
	"time"

	"github.com/bean-la/wrklogr/internal/config"
	"github.com/bean-la/wrklogr/internal/session"
)

func TestFilterDaysByRepoRecalculatesFuzzyHours(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.April, 12, 9, 0, 0, 0, time.UTC)
	days := []session.DaySummary{
		{
			Day: "2026-04-12",
			Sessions: []session.Session{
				{
					Commits: []session.Commit{
						{Repo: "bean-la/shtack", SHA: "a", Timestamp: base},
						{Repo: "Third-Eye-Tarot/rainbow-mono", SHA: "b", Timestamp: base.Add(30 * time.Minute)},
						{Repo: "bean-la/slyce-studio", SHA: "c", Timestamp: base.Add(9 * time.Hour)},
					},
					Start:      base,
					End:        base.Add(9 * time.Hour),
					FuzzyHours: 9,
				},
			},
			TotalHours: 9,
		},
	}

	nc := &config.NokoConfig{
		RepoProjects: map[string]config.RepoProject{
			"bean-la/shtack":                 {ProjectID: 687237},
			"bean-la/slyce-studio":           {ProjectID: 687237},
			"Third-Eye-Tarot/rainbow-mono":   {ProjectID: 708823},
		},
	}

	full := aggregateByProject(days, nc)
	if got := full[708823].Minutes; got != 270 {
		t.Fatalf("unfiltered third eye minutes: got %d, want 270 (half of 9h)", got)
	}

	filtered := filterDaysByRepo(days, []string{"Third-Eye-Tarot/*"})
	only := aggregateByProject(filtered, nc)
	if got := only[708823].Minutes; got != 60 {
		t.Fatalf("filtered third eye minutes: got %d, want 60 (1h from single commit span)", got)
	}
}
