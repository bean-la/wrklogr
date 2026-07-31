package session

import (
	"testing"
	"time"
)

func TestBuildClustersAcrossReposWithGapAndCeil(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.January, 1, 9, 0, 0, 0, time.UTC)
	commits := []Commit{
		{Repo: "a/one", SHA: "1", Timestamp: base},
		{Repo: "b/two", SHA: "2", Timestamp: base.Add(75 * time.Minute)},
		{Repo: "a/one", SHA: "3", Timestamp: base.Add(4 * time.Hour)},
	}

	sessions := Build(commits, 2*time.Hour)
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}

	if got := sessions[0].FuzzyHours; got != 2 {
		t.Fatalf("expected first session fuzzy hours 2, got %d", got)
	}
	if got := len(sessions[0].Commits); got != 2 {
		t.Fatalf("expected first session to include 2 commits, got %d", got)
	}
	if got := sessions[1].FuzzyHours; got != 1 {
		t.Fatalf("expected single-commit session to round to 1 hour, got %d", got)
	}
}

func TestSessionFromCommitsFuzzySpan(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.April, 1, 9, 0, 0, 0, time.UTC)
	sess, ok := SessionFromCommits([]Commit{
		{Repo: "org/a", SHA: "1", Timestamp: base},
		{Repo: "org/b", SHA: "2", Timestamp: base.Add(90 * time.Minute)},
	})
	if !ok {
		t.Fatal("expected session")
	}
	if sess.FuzzyHours != 2 {
		t.Fatalf("expected 2 fuzzy hours, got %d", sess.FuzzyHours)
	}
}

func TestBucketByDayUsesConfiguredTimezone(t *testing.T) {
	t.Parallel()

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}

	sessions := []Session{
		{
			Start:      time.Date(2026, time.January, 2, 1, 30, 0, 0, time.UTC), // Jan 1 local
			End:        time.Date(2026, time.January, 2, 2, 0, 0, 0, time.UTC),
			FuzzyHours: 1,
		},
		{
			Start:      time.Date(2026, time.January, 2, 16, 0, 0, 0, time.UTC), // Jan 2 local
			End:        time.Date(2026, time.January, 2, 17, 0, 0, 0, time.UTC),
			FuzzyHours: 2,
		},
	}

	days := BucketByDay(sessions, loc)
	if len(days) != 2 {
		t.Fatalf("expected 2 day buckets, got %d", len(days))
	}

	if days[0].Day != "2026-01-01" {
		t.Fatalf("expected first bucket 2026-01-01, got %q", days[0].Day)
	}
	if days[1].Day != "2026-01-02" {
		t.Fatalf("expected second bucket 2026-01-02, got %q", days[1].Day)
	}
}
