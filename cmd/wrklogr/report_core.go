package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/bean-la/wrklogr/internal/config"
	ghclient "github.com/bean-la/wrklogr/internal/github"
	gcal "github.com/bean-la/wrklogr/internal/gcal"
	"github.com/bean-la/wrklogr/internal/llm"
	"github.com/bean-la/wrklogr/internal/noko"
	"github.com/bean-la/wrklogr/internal/session"
)

type nokoEntry struct {
	Date        string
	Minutes     int
	ProjectID   int
	Description string
}

type reportOpts struct {
	Repos         []string
	Since         *time.Time
	Until         *time.Time
	Author        string
	SessionGap    time.Duration
	Timezone      *time.Location
	GCalFlag      bool
	GCalCalendar  string
	NokoDryRun    bool
	NokoPush      bool
	NokoToken     string
	NokoConfig    *config.NokoConfig
	LLMSummarize  bool
	LLMConfig     *llm.Config
	GitHubToken   string
	MinLogMinutes int
}

type reportResult struct {
	Days         []session.DaySummary
	NokoEntries  []nokoEntry
	TotalCommits int
}

func runReport(ctx context.Context, out interface{ Write([]byte) (int, error) }, opts reportOpts) (*reportResult, error) {
	merged := make([]session.Commit, 0, 128)
	total := 0

	ghClient := ghclient.NewClient(opts.GitHubToken, nil)

	var viewer *ghclient.ViewerIdentity
	if opts.Author != "" {
		viewer = &ghclient.ViewerIdentity{
			Login:  opts.Author,
			Emails: map[string]struct{}{},
		}
	}

	for _, fullRepo := range opts.Repos {
		owner, repoName, parseErr := splitRepo(fullRepo)
		if parseErr != nil {
			return nil, parseErr
		}

		commits, fetchErr := ghClient.ListCommits(ctx, owner, repoName, opts.Since, opts.Until)
		if fetchErr != nil {
			return nil, fetchErr
		}
		filtered := 0
		for _, c := range commits {
			if viewer != nil && !commitMatchesViewer(c, viewer) {
				continue
			}
			normalized, ok := normalizeCommit(fullRepo, c)
			if !ok {
				continue
			}
			merged = append(merged, normalized)
			filtered++
		}
		total += filtered
		fmt.Fprintf(out, "%s: %d commits\n", fullRepo, filtered)
	}

	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Timestamp.Before(merged[j].Timestamp)
	})
	sessions := session.Build(merged, opts.SessionGap)
	days := session.BucketByDay(sessions, opts.Timezone)

	if opts.GCalFlag && opts.GCalCalendar != "" {
		gcalSince := opts.Since
		gcalUntil := opts.Until
		if gcalSince == nil {
			t := time.Now().AddDate(0, -1, 0)
			gcalSince = &t
		}
		if gcalUntil == nil {
			t := time.Now()
			gcalUntil = &t
		}

		events, err := gcal.FetchEvents(opts.GCalCalendar, *gcalSince, *gcalUntil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "calendar fetch: %v\n", err)
		}

		if len(events) > 0 {
			fmt.Fprintf(out, "calendar: %d events\n", len(events))
			dayMap := make(map[string]*session.DaySummary)
			for i := range days {
				dayMap[days[i].Day] = &days[i]
			}
			for _, ev := range events {
				dayKey := ev.Start.Format("2006-01-02")
				minutes := int(ev.Length.Minutes())
				if minutes < 1 {
					minutes = 1
				}
				fuzzyHours := (minutes + 59) / 60
				sess := session.Session{
					Commits: []session.Commit{{
						Repo:      "📅 " + ev.Title,
						SHA:       "",
						Message:   ev.Title,
						Timestamp: ev.Start,
					}},
					Start:      ev.Start,
					End:        ev.End,
					FuzzyHours: fuzzyHours,
				}
				if ds, ok := dayMap[dayKey]; ok {
					ds.Sessions = append(ds.Sessions, sess)
					ds.TotalHours += fuzzyHours
				}
			}
		}
	}

	var entries []nokoEntry
	if opts.NokoDryRun || opts.NokoPush {
		nokoClient := noko.NewClient(opts.NokoToken, nil)

		var llmClient *llm.Client
		if opts.LLMSummarize && opts.LLMConfig != nil && opts.LLMConfig.APIKey != "" {
			llmClient = llm.NewClient(*opts.LLMConfig)
		}

		for _, day := range days {
			for _, sess := range day.Sessions {
				repos := getUniqueReposForSession(sess)
				groups := groupByProject(repos, opts.NokoConfig)
				minutesPerGroup := sess.FuzzyHours * 60 / len(groups)
				if minutesPerGroup < 1 {
					minutesPerGroup = 1
				}
				effectiveMin := opts.MinLogMinutes
				if effectiveMin == 0 && opts.NokoConfig != nil && opts.NokoConfig.MinLogMinutes > 0 {
					effectiveMin = opts.NokoConfig.MinLogMinutes
				}
				if effectiveMin > 0 && minutesPerGroup < effectiveMin {
					minutesPerGroup = effectiveMin
				}
				for projID, projRepos := range groups {
					if projID == 0 {
						continue
					}
					summary := sessionSummary(sess, projRepos, llmClient)
					repoLabel := strings.Join(projRepos, ", ")
					if len(sess.Commits) > 0 {
						repoLabel += fmt.Sprintf(" (%d commits)", len(sess.Commits))
					}
					var desc string
					if summary != "" {
						desc = summary + " [" + repoLabel + "]"
					} else {
						desc = repoLabel
					}
					sourceURL := nokoSourceURL(opts.Author, day.Day, projID, minutesPerGroup, desc)
					if opts.NokoDryRun && !opts.NokoPush {
						entries = append(entries, nokoEntry{
							Date:        day.Day,
							Minutes:     minutesPerGroup,
							ProjectID:   projID,
							Description: desc,
						})
					} else if opts.NokoPush {
						entry := noko.EntryRequest{
							Date:        day.Day,
							Minutes:     minutesPerGroup,
							Description: desc,
							ProjectID:   projID,
							SourceURL:   sourceURL,
						}
						if err := nokoClient.CreateEntry(ctx, entry); err != nil {
							if err == noko.ErrDuplicate {
								fmt.Fprintf(os.Stderr, "skipping duplicate noko entry %s project=%d\n", day.Day, projID)
								continue
							}
							return nil, fmt.Errorf("push session %s %s: %w", day.Day, desc, err)
						}
					}
				}
			}
		}
	}

	return &reportResult{
		Days:         days,
		NokoEntries:  entries,
		TotalCommits: total,
	}, nil
}

type nameMap func(id int) string

// nokoSourceURL returns a stable identifier for a Noko entry used to skip duplicates on re-runs.
// Excludes description so LLM summarization doesn't break dedup across runs.
func nokoSourceURL(author, date string, projectID, minutes int, _ string) string {
	return fmt.Sprintf("wrklogr://%s/%s/%d/%d", author, date, projectID, minutes)
}
