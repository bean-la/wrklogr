package session

import (
	"math"
	"sort"
	"time"
)

type Commit struct {
	Repo      string
	SHA       string
	Message   string
	Timestamp time.Time
}

type Session struct {
	Commits    []Commit
	Start      time.Time
	End        time.Time
	FuzzyHours int
}

type DaySummary struct {
	Day        string
	Sessions   []Session
	TotalHours int
}

func Build(commits []Commit, sessionGap time.Duration) []Session {
	if len(commits) == 0 {
		return nil
	}

	sorted := make([]Commit, 0, len(commits))
	for _, c := range commits {
		if c.Timestamp.IsZero() {
			continue
		}
		sorted = append(sorted, c)
	}
	if len(sorted) == 0 {
		return nil
	}

	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Timestamp.Before(sorted[j].Timestamp)
	})

	out := make([]Session, 0, len(sorted))
	current := []Commit{sorted[0]}
	for i := 1; i < len(sorted); i++ {
		prev := sorted[i-1]
		next := sorted[i]
		if next.Timestamp.Sub(prev.Timestamp) > sessionGap {
			out = append(out, makeSession(current))
			current = []Commit{next}
			continue
		}
		current = append(current, next)
	}
	out = append(out, makeSession(current))
	return out
}

func BucketByDay(sessions []Session, loc *time.Location) []DaySummary {
	if len(sessions) == 0 {
		return nil
	}
	if loc == nil {
		loc = time.Local
	}

	dayMap := map[string]*DaySummary{}
	for _, s := range sessions {
		key := s.Start.In(loc).Format("2006-01-02")
		day, ok := dayMap[key]
		if !ok {
			day = &DaySummary{Day: key}
			dayMap[key] = day
		}
		day.Sessions = append(day.Sessions, s)
		day.TotalHours += s.FuzzyHours
	}

	keys := make([]string, 0, len(dayMap))
	for k := range dayMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]DaySummary, 0, len(keys))
	for _, k := range keys {
		out = append(out, *dayMap[k])
	}
	return out
}

func makeSession(commits []Commit) Session {
	start := commits[0].Timestamp
	end := commits[len(commits)-1].Timestamp
	hours := int(math.Ceil(end.Sub(start).Hours()))
	if hours == 0 {
		hours = 1
	}
	return Session{
		Commits:    commits,
		Start:      start,
		End:        end,
		FuzzyHours: hours,
	}
}

// SessionFromCommits builds a single session from commits (sorted by timestamp).
// FuzzyHours are derived from the span of the given commits only, matching makeSession.
// Used after repo filtering so invoice hours stay aligned with Noko push splits.
func SessionFromCommits(commits []Commit) (Session, bool) {
	if len(commits) == 0 {
		return Session{}, false
	}
	sorted := make([]Commit, len(commits))
	copy(sorted, commits)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Timestamp.Before(sorted[j].Timestamp)
	})
	return makeSession(sorted), true
}
