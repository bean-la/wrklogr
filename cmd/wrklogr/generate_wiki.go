package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bean-la/wrklogr/internal/config"
	ghclient "github.com/bean-la/wrklogr/internal/github"
	"github.com/bean-la/wrklogr/internal/noko"
	"github.com/bean-la/wrklogr/internal/session"
	"github.com/spf13/cobra"
)

func newGenerateWikiCmd(getConfig func() (*config.RuntimeConfig, error)) *cobra.Command {
	var orgsInput []string
	var authorsInput []string
	var sinceInput string
	var untilInput string
	var outputDir string
	var cacheDir string
	var showCommits bool
	var llmSummarize bool
	var nokoDryRun bool
	var nokoPush bool
	var nokoToken string
	var gcalFlag bool
	var token string

	cmd := &cobra.Command{
		Use:   "generate-wiki",
		Short: "Generate monthly wiki pages from org-based repo discovery",
		Long: `Discover repos from GitHub orgs, fetch commits for each author, run
noko dry-run, and generate per-author wiki pages with project summaries.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := getConfig()
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if cfg == nil {
				cfg = &config.RuntimeConfig{
					SessionGap: 2 * time.Hour,
					Timezone:   time.UTC,
				}
			}

			since, err := parseDateBound(sinceInput, false)
			if err != nil {
				return fmt.Errorf("parse --since: %w", err)
			}
			until, err := parseDateBound(untilInput, false)
			if err != nil {
				return fmt.Errorf("parse --until: %w", err)
			}

			if since == nil || until == nil {
				return fmt.Errorf("--since and --until are required")
			}

			sinceFmt := since.Format("2006-01-02")
			monthLabel := since.Format("2006-01")
			untilFmt := until.Format("2006-01-02")
			nowUTC := time.Now().UTC().Format("2006-01-02 15:04 UTC")

			if outputDir == "" {
				outputDir = "/tmp/wiki-pages"
			}

			if err := os.MkdirAll(filepath.Join(outputDir, monthLabel), 0755); err != nil {
				return fmt.Errorf("create output dir: %w", err)
			}

			// Try to load a cache file for this month
			var cacheLoaded *cacheFile
			if cacheDir != "" {
				cachePath := filepath.Join(cacheDir, monthLabel+".json")
				if data, err := os.ReadFile(cachePath); err == nil {
					var cf cacheFile
					if json.Unmarshal(data, &cf) == nil {
						cacheLoaded = &cf
						fmt.Fprintf(cmd.OutOrStdout(), "loaded cache: %d days, %d noko entries\n", len(cf.Days), len(cf.NokoEntries))
					}
				}
			}

			cacheCoversRange := false
			if cacheLoaded != nil {
				cacheEnd := ""
				for _, d := range cacheLoaded.Days {
					if d.Day > cacheEnd {
						cacheEnd = d.Day
					}
				}
				if cacheEnd >= untilFmt {
					cacheCoversRange = true
				}
			}

			var allDays []session.DaySummary
			var allNoko []nokoEntry

			if cacheCoversRange && len(cacheLoaded.NokoEntries) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "cache covers full range, skipping API calls\n")
				allNoko = make([]nokoEntry, len(cacheLoaded.NokoEntries))
				for i, e := range cacheLoaded.NokoEntries {
					allNoko[i] = nokoEntry{
						Date: e.Date, Minutes: e.Minutes, ProjectID: e.ProjectID, Description: e.Description,
					}
				}
				dayMap := make(map[string]*session.DaySummary)
				for _, cd := range cacheLoaded.Days {
					ds := &session.DaySummary{Day: cd.Day, TotalHours: cd.Hours}
					for _, cs := range cd.Sessions {
						ds.Sessions = append(ds.Sessions, session.Session{
							FuzzyHours: cs.Hours,
							Commits:    []session.Commit{{Repo: strings.Join(cs.Repos, ", ")}},
						})
					}
					if existing, ok := dayMap[cd.Day]; ok {
						existing.TotalHours += ds.TotalHours
						existing.Sessions = append(existing.Sessions, ds.Sessions...)
					} else {
						dayMap[cd.Day] = ds
					}
				}
				var keys []string
				for k := range dayMap {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					allDays = append(allDays, *dayMap[k])
				}
			} else {
				authToken := resolveToken(token)

				repos, err := discoverReposFromOrgs(context.Background(), authToken, orgsInput)
				if err != nil {
					return fmt.Errorf("discover repos from orgs: %w", err)
				}
				if len(repos) == 0 {
					fmt.Fprintf(cmd.ErrOrStderr(), "no repos found in orgs %v\n", orgsInput)
					return nil
				}

				repos = filterReposByAuthors(context.Background(), authToken, repos, authorsInput, since, until)
				if len(repos) == 0 {
					fmt.Fprintf(cmd.ErrOrStderr(), "no repos with commits from authors %v in range\n", authorsInput)
					return nil
				}

				sessionGap := cfg.SessionGap
				reportTZ := cfg.Timezone
				nokoCfg := cfg.Noko
				llmCfg := resolveLLMConfig(cfg, "", "")
				gcalCal := ""
				if cfg.GCal != nil {
					gcalCal = cfg.GCal.Calendar
				}

				for _, author := range authorsInput {
					fmt.Fprintf(cmd.OutOrStdout(), "\n─── %s ───\n", author)

					opts := reportOpts{
						Repos:         repos,
						Since:         since,
						Until:         until,
						Author:        author,
						SessionGap:    sessionGap,
						Timezone:      reportTZ,
						GCalFlag:      gcalFlag,
						GCalCalendar:  gcalCal,
						NokoDryRun:    nokoDryRun || nokoPush, // accumulate entries; post-process block handles push
						NokoPush:      false,
						NokoConfig:    nokoCfg,
						LLMSummarize:  llmSummarize,
						LLMConfig:     &llmCfg,
						GitHubToken:   authToken,
						MinLogMinutes: nokoMinLog(nokoCfg),
					}

					result, runErr := runReport(context.Background(), cmd.OutOrStdout(), opts)
					if runErr != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "report for %s: %v\n", author, runErr)
						continue
					}

					allDays = append(allDays, result.Days...)
					allNoko = append(allNoko, result.NokoEntries...)
				}
			}

			// Push allNoko entries to Noko if requested (handles both cache and API paths)
			if nokoPush && len(allNoko) > 0 {
				resolvedNokoToken := strings.TrimSpace(nokoToken)
				if resolvedNokoToken == "" {
					resolvedNokoToken = strings.TrimSpace(os.Getenv("NOKO_TOKEN"))
				}
				if resolvedNokoToken == "" && cfg.Noko != nil {
					resolvedNokoToken = strings.TrimSpace(cfg.Noko.APIToken)
				}
				nokoClient := noko.NewClient(resolvedNokoToken, nil)
				for _, entry := range allNoko {
					srcURL := nokoSourceURL("", entry.Date, entry.ProjectID, entry.Minutes, entry.Description)
					req := noko.EntryRequest{
						Date:        entry.Date,
						Minutes:     entry.Minutes,
						Description: entry.Description,
						ProjectID:   entry.ProjectID,
						SourceURL:   srcURL,
					}
					if err := nokoClient.CreateEntry(context.Background(), req); err != nil {
						if err == noko.ErrDuplicate {
							fmt.Fprintf(cmd.ErrOrStderr(), "skipping duplicate noko entry %s project=%d\n", entry.Date, entry.ProjectID)
							continue
						}
						fmt.Fprintf(cmd.ErrOrStderr(), "noko push %s project=%d: %v\n", entry.Date, entry.ProjectID, err)
					}
				}
			}

			// Build per-project daily logs and hours from noko entries
			type projectData struct {
				hours int
				lines []string
			}
			projects := make(map[int]*projectData)

			for _, entry := range allNoko {
				pd, ok := projects[entry.ProjectID]
				if !ok {
					pd = &projectData{}
					projects[entry.ProjectID] = pd
				}
				hours := (entry.Minutes + 59) / 60
				pd.hours += hours
				pd.lines = append(pd.lines, fmt.Sprintf("  - %s · %dm · %s", entry.Date, entry.Minutes, entry.Description))
			}

			ids := make([]int, 0, len(projects))
			for id := range projects {
				ids = append(ids, id)
			}
			sort.Ints(ids)

			// Merge days by key
			dayMap := make(map[string]*session.DaySummary)
			for i := range allDays {
				d := &allDays[i]
				if existing, ok := dayMap[d.Day]; ok {
					existing.TotalHours += d.TotalHours
					existing.Sessions = append(existing.Sessions, d.Sessions...)
				} else {
					copy := *d
					dayMap[d.Day] = &copy
				}
			}
			var sortedDayKeys []string
			for k := range dayMap {
				sortedDayKeys = append(sortedDayKeys, k)
			}
			sort.Strings(sortedDayKeys)

			// Build wiki pages per author
			for _, author := range authorsInput {
				var sb strings.Builder

				sb.WriteString(fmt.Sprintf("# %s — %s\n\n", author, monthLabel))
				sb.WriteString(fmt.Sprintf("Period: %s → %s\n\n", sinceFmt, untilFmt))

				// Project summary table
				if len(projects) > 0 {
					sb.WriteString("## Summary\n\n")
					sb.WriteString("| Project | Hours | Days |\n")
					sb.WriteString("|---------|-------|------|\n")
					totalHours := 0
					totalDays := 0.0
					for _, id := range ids {
						pd := projects[id]
						name := projectName(id)
						days := float64(pd.hours) / 8.0
						totalDays += days
						sb.WriteString(fmt.Sprintf("| %s | %dh | %.2f |\n", name, pd.hours, days))
						totalHours += pd.hours
					}
					sb.WriteString(fmt.Sprintf("| **Total** | **%dh** | **%.2f** |\n", totalHours, totalDays))
					sb.WriteString("\n---\n\n")
				}

				// Day-by-day commit sessions
				for _, dayKey := range sortedDayKeys {
					day := dayMap[dayKey]
					hasWork := false
					for _, sess := range day.Sessions {
						if isCalendarSession(sess) {
							continue
						}
						hasWork = true
						break
					}
					if !hasWork {
						continue
					}
					sb.WriteString(fmt.Sprintf("## %s · %dh\n\n", day.Day, day.TotalHours))
					for _, sess := range day.Sessions {
						repos := getUniqueReposForSession(sess)
						sb.WriteString(fmt.Sprintf("- %dh [%s]", sess.FuzzyHours, strings.Join(repos, ", ")))
						if showCommits && len(sess.Commits) > 0 {
							sb.WriteString("\n")
							for _, c := range sess.Commits {
								msg := strings.SplitN(c.Message, "\n", 2)[0]
								if len(msg) > 60 {
									msg = msg[:60] + "..."
								}
								sha := ""
								if len(c.SHA) >= 8 {
									sha = c.SHA[:8]
								}
								if sha != "" {
									sb.WriteString(fmt.Sprintf("  - %s %s\n", sha, msg))
								}
							}
						} else {
							sb.WriteString("\n")
						}
					}
					sb.WriteString("\n")
				}

				// Per-project daily noko entries
				if len(projects) > 0 {
					for _, id := range ids {
						pd := projects[id]
						sb.WriteString(fmt.Sprintf("## %s\n\n", projectName(id)))
						for _, line := range pd.lines {
							sb.WriteString(line + "\n")
						}
						sb.WriteString("\n")
					}
				}

				sb.WriteString("---\n")
				sb.WriteString(fmt.Sprintf("_Generated by wrklogr on %s_\n", nowUTC))

				pageFile := filepath.Join(outputDir, monthLabel, author+".md")
				if err := os.WriteFile(pageFile, []byte(sb.String()), 0644); err != nil {
					return fmt.Errorf("write page %s: %w", pageFile, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", pageFile)
			}

			// Write updated cache
			if cacheDir != "" {
				if err := os.MkdirAll(cacheDir, 0755); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "create cache dir: %v\n", err)
				} else {
					var cd []cachedDay
					for _, dayKey := range sortedDayKeys {
						day := dayMap[dayKey]
						d := cachedDay{Day: day.Day, Hours: day.TotalHours}
						for _, s := range day.Sessions {
							if isCalendarSession(s) {
								continue
							}
							d.Sessions = append(d.Sessions, cachedSession{
								Hours: s.FuzzyHours,
								Repos: getUniqueReposForSession(s),
							})
						}
						cd = append(cd, d)
					}
					var cn []cachedNokoEntry
					for _, e := range allNoko {
						cn = append(cn, cachedNokoEntry{
							Date: e.Date, Minutes: e.Minutes, ProjectID: e.ProjectID, Description: e.Description,
						})
					}
					cf := cacheFile{
						Version: 1, Month: monthLabel, Since: sinceFmt, Until: untilFmt,
						Updated: time.Now().UTC().Format(time.RFC3339), Authors: authorsInput,
						Days: cd, NokoEntries: cn,
					}
					cachePath := filepath.Join(cacheDir, monthLabel+".json")
					data, _ := json.MarshalIndent(cf, "", "  ")
					if err := os.WriteFile(cachePath, data, 0644); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "write cache: %v\n", err)
					} else {
						fmt.Fprintf(cmd.OutOrStdout(), "wrote cache %s\n", cachePath)
					}
				}
			}

			// Write metadata files
			os.WriteFile(filepath.Join(outputDir, ".month_label"), []byte(monthLabel), 0644)
			os.WriteFile(filepath.Join(outputDir, ".since"), []byte(sinceFmt), 0644)
			os.WriteFile(filepath.Join(outputDir, ".until"), []byte(untilFmt), 0644)
			os.WriteFile(filepath.Join(outputDir, ".authors"), []byte(strings.Join(authorsInput, " ")), 0644)

			return nil
		},
	}

	cmd.Flags().StringSliceVar(&orgsInput, "orgs", nil, "GitHub orgs to scan for repos")
	cmd.Flags().StringSliceVar(&authorsInput, "authors", nil, "GitHub logins to track")
	cmd.Flags().StringVar(&sinceInput, "since", "", "Start date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&untilInput, "until", "", "End date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&outputDir, "output", "", "Output directory for wiki pages (default /tmp/wiki-pages)")
	cmd.Flags().StringVar(&cacheDir, "cache-dir", "", "Cache directory for incremental runs (default: none)")
	cmd.Flags().BoolVar(&showCommits, "show-commits", false, "Show individual commits")
	cmd.Flags().BoolVar(&llmSummarize, "llm-summarize", false, "Use LLM to summarize sessions")
	cmd.Flags().BoolVar(&nokoDryRun, "noko-dry-run", false, "Include Noko dry-run entries")
	cmd.Flags().BoolVar(&nokoPush, "push-noko", false, "Push sessions to Noko")
	cmd.Flags().StringVar(&nokoToken, "noko-token", "", "Noko API token (defaults to NOKO_TOKEN env var or noko.api_token in config)")
	cmd.Flags().BoolVar(&gcalFlag, "gcal", false, "Include Google Calendar events")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token")

	return cmd
}

func discoverReposFromOrgs(ctx context.Context, token string, orgs []string) ([]string, error) {
	client := ghclient.NewClient(token, nil)
	seen := make(map[string]struct{})
	var repos []string

	for _, org := range orgs {
		orgRepos, err := client.ListOrgRepos(ctx, org)
		if err != nil {
			return nil, fmt.Errorf("org %s: %w", org, err)
		}
		for _, r := range orgRepos {
			if _, ok := seen[r]; !ok {
				seen[r] = struct{}{}
				repos = append(repos, r)
			}
		}
	}
	sort.Strings(repos)
	return repos, nil
}

func filterReposByAuthors(ctx context.Context, token string, repos, authors []string, since, until *time.Time) []string {
	client := ghclient.NewClient(token, nil)
	var filtered []string

	for _, fullRepo := range repos {
		owner, repoName, err := splitRepo(fullRepo)
		if err != nil {
			continue
		}
		for _, author := range authors {
			match, err := client.HasCommitsByAuthor(ctx, owner, repoName, author, since, until)
			if err != nil {
				continue
			}
			if match != nil {
				filtered = append(filtered, fullRepo)
				break
			}
		}
	}
	return filtered
}

func nokoMinLog(nc *config.NokoConfig) int {
	if nc == nil {
		return 0
	}
	return nc.MinLogMinutes
}

func projectName(id int) string {
	switch id {
	case 687237:
		return "Bean"
	case 708823:
		return "Third Eye"
	case 716638:
		return "Dear Freeda (TMV)"
	case 586501:
		return "Dublab"
	case 611157:
		return "Salon 94"
	case 560046:
		return "Jono Pandolfi"
	case 607240:
		return "Farm To People"
	case 557928:
		return "Culinistas"
	case 595328:
		return "Minisocial"
	case 615238:
		return "Tripoli Gallery"
	case 606165:
		return "Max Levai"
	case 662679:
		return "Ghia"
	case 639789:
		return "Syng"
	case 590606:
		return "Tartine"
	default:
		return fmt.Sprintf("Project %d", id)
	}
}

func isCalendarSession(s session.Session) bool {
	if len(s.Commits) == 0 {
		return false
	}
	return strings.HasPrefix(s.Commits[0].Repo, "📅 ")
}

func resolveToken(flagToken string) string {
	if flagToken != "" {
		return flagToken
	}
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	if t := os.Getenv("GH_TOKEN"); t != "" {
		return t
	}
	return ""
}
