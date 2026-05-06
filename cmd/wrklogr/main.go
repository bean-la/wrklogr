package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bean-la/wrklogr/internal/config"
	ghclient "github.com/bean-la/wrklogr/internal/github"
	gcal "github.com/bean-la/wrklogr/internal/gcal"
	"github.com/bean-la/wrklogr/internal/llm"
	"github.com/bean-la/wrklogr/internal/localgit"
	noko "github.com/bean-la/wrklogr/internal/noko"
	"github.com/bean-la/wrklogr/internal/session"
	gh "github.com/google/go-github/v67/github"
	"github.com/spf13/cobra"
)

// version is set at link time via:
//
//	go build -ldflags "-X main.version=v0.1.0"
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var configPath string
	var cfg *config.RuntimeConfig

	root := &cobra.Command{
		Use:   "wrklogr",
		Short: "Build a worklog from GitHub commits across private repos",
		Long: `wrklogr fetches commits from configured GitHub repositories, clusters them
into work sessions, and emits Markdown (and optionally JSON) reports.`,
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Name() == "version" || cmd.Name() == "onboard" {
				return nil
			}
			loaded, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			cfg = loaded
			return nil
		},
	}

	root.PersistentFlags().StringVar(&configPath, "config", "", "Path to wrklogr TOML config")

	reportCmd := newReportCmd(func() (*config.RuntimeConfig, error) {
		if cfg == nil {
			return nil, fmt.Errorf("config is not loaded")
		}
		return cfg, nil
	})

	root.AddCommand(newVersionCmd())
	root.AddCommand(reportCmd)
	root.AddCommand(newInstallWorkflowCmd())
	root.AddCommand(newOnboardCmd())
	root.AddCommand(newReviewCmd(func() (*config.RuntimeConfig, error) {
		if cfg == nil {
			return nil, fmt.Errorf("config is not loaded")
		}
		return cfg, nil
	}))

	// Default to running report when no subcommand is provided
	root.RunE = func(cmd *cobra.Command, args []string) error {
		return reportCmd.RunE(reportCmd, args)
	}

	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the embedded build version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version)
		},
	}
}

func newReportCmd(getConfig func() (*config.RuntimeConfig, error)) *cobra.Command {
	var sinceInput string
	var untilInput string
	var token string
	var meOnly bool
	var authorFilter string
	var sessionGapInput string
	var timezoneInput string
	var localMode bool = true
	var localPaths []string
	var discoverSubmodules bool
	var maxDepth int = 3
	var showCommits bool
	var pushNoko bool
	var nokoDryRun bool
	var nokoTokenInput string
	var minLogMinutes int
	var llmSummarize bool
	var llmAPIKey string
	var llmModel string
	var gcalFlag bool
	var reposInput []string

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Fetch commits for configured repositories",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := getConfig()
			if err != nil {
				// If config file doesn't exist, that's OK for local mode
				if !os.IsNotExist(err) {
					return err
				}
				cfg = &config.RuntimeConfig{
					SessionGap: 2 * time.Hour,
					Timezone:   time.UTC,
				}
			}

			// Auto-set since to beginning of current month if not specified and today is after the 20th
			if sinceInput == "" {
				now := time.Now()
				if now.Day() > 20 {
					// Set to the first day of the current month at midnight
					beginningOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
					sinceInput = beginningOfMonth.Format("2006-01-02")
				}
			}

			since, err := parseDateBound(sinceInput, false)
			if err != nil {
				return fmt.Errorf("parse --since: %w", err)
			}
			until, err := parseDateBound(untilInput, true)
			if err != nil {
				return fmt.Errorf("parse --until: %w", err)
			}

			if since != nil && until != nil && since.After(*until) {
				return fmt.Errorf("--since must be before or equal to --until")
			}

			sessionGap := cfg.SessionGap
			if strings.TrimSpace(sessionGapInput) != "" {
				parsed, parseErr := time.ParseDuration(strings.TrimSpace(sessionGapInput))
				if parseErr != nil {
					return fmt.Errorf("parse --session-gap: %w", parseErr)
				}
				if parsed <= 0 {
					return fmt.Errorf("--session-gap must be > 0")
				}
				sessionGap = parsed
			}

			reportTZ := cfg.Timezone
			if strings.TrimSpace(timezoneInput) != "" {
				loadedTZ, loadErr := time.LoadLocation(strings.TrimSpace(timezoneInput))
				if loadErr != nil {
					return fmt.Errorf("parse --timezone: %w", loadErr)
				}
				reportTZ = loadedTZ
			}

			authToken := strings.TrimSpace(token)
			if authToken == "" {
				authToken = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
			}
			if authToken == "" {
				authToken = strings.TrimSpace(os.Getenv("GH_TOKEN"))
			}
			if authToken == "" {
				// Try to get token from gh CLI
				if cmd, err := exec.Command("gh", "auth", "token").Output(); err == nil {
					authToken = strings.TrimSpace(string(cmd))
				}
			}
			if !localMode && authToken == "" {
				localMode = true
				if len(localPaths) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No GitHub token found; falling back to local git mode.")
				}
			}

			merged := make([]session.Commit, 0, 128)
			total := 0
			if localMode {
				paths := localPaths
				if len(paths) == 0 {
					paths = []string{"."}
				}

				// Discover git repositories in subdirectories if requested
				if discoverSubmodules {
					discovered, err := discoverGitRepositories(paths, maxDepth)
					if err != nil {
						return fmt.Errorf("discover git repositories: %w", err)
					}
					if len(discovered) > 0 {
						// Merge discovered paths with original paths, avoiding duplicates
						seen := make(map[string]struct{})
						for _, p := range paths {
							absP, err := filepath.Abs(p)
							if err == nil {
								seen[absP] = struct{}{}
							}
						}
						for _, d := range discovered {
							absD, err := filepath.Abs(d)
							if err == nil {
								if _, exists := seen[absD]; !exists {
									paths = append(paths, d)
									seen[absD] = struct{}{}
								}
							}
						}
						fmt.Fprintf(cmd.OutOrStdout(), "Discovered %d git repositories\n", len(discovered))
					}
				}
				emailSet := map[string]struct{}{}
				if meOnly {
					for _, p := range paths {
						for email := range localgit.CurrentEmails(p) {
							emailSet[email] = struct{}{}
						}
					}
					if len(emailSet) == 0 {
						return fmt.Errorf("--me with --local requires git user.email in repo or global git config")
					}
				}

				for _, p := range paths {
					commits, err := localgit.ListCommits(p, since, until)
					if err != nil {
						return err
					}
					repoLabel := repoLabelForPath(p)
					filtered := 0
					for _, c := range commits {
						if meOnly && !localCommitMatchesMe(c, emailSet) {
							continue
						}
						if authorFilter != "" && !strings.EqualFold(c.AuthorName, authorFilter) {
							continue
						}
						ts := c.AuthorDate
						if ts.IsZero() {
							ts = c.CommitterDate
						}
						if ts.IsZero() {
							continue
						}
						merged = append(merged, session.Commit{
							Repo:      repoLabel,
							SHA:       c.SHA,
							Message:   c.Subject,
							Timestamp: ts,
						})
						filtered++
					}
					total += filtered
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %d commits\n", repoLabel, filtered)
				}
			} else {
				repos := cfg.Repos
				if len(reposInput) > 0 {
					repos = reposInput
				}
				if len(repos) == 0 {
					return fmt.Errorf("no repositories configured; set repos in wrklogr.toml or use --repos")
				}

				client := ghclient.NewClient(authToken, nil)
				ctx := context.Background()

				var viewer *ghclient.ViewerIdentity
				if meOnly {
					identity, identityErr := client.GetViewerIdentity(ctx)
					if identityErr != nil {
						return fmt.Errorf("resolve --me identity: %w", identityErr)
					}
					viewer = identity
				} else if authorFilter != "" {
					viewer = &ghclient.ViewerIdentity{
						Login:  authorFilter,
						Emails: map[string]struct{}{},
					}
				}

				for _, fullRepo := range repos {
					owner, repoName, parseErr := splitRepo(fullRepo)
					if parseErr != nil {
						return parseErr
					}

					commits, fetchErr := client.ListCommits(ctx, owner, repoName, since, until)
					if fetchErr != nil {
						return fetchErr
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
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %d commits\n", fullRepo, filtered)
				}
			}

			sort.Slice(merged, func(i, j int) bool {
				return merged[i].Timestamp.Before(merged[j].Timestamp)
			})
			sessions := session.Build(merged, sessionGap)
			days := session.BucketByDay(sessions, reportTZ)

			if gcalFlag {
				gcalCal := "seb@bean.la"
				if cfg.GCal != nil && cfg.GCal.Calendar != "" {
					gcalCal = cfg.GCal.Calendar
				}

				gcalSince := time.Now().AddDate(0, -1, 0)
				gcalUntil := time.Now()
				if since != nil {
					gcalSince = *since
				}
				if until != nil {
					gcalUntil = *until
				}

				events, err := gcal.FetchEvents(gcalCal, gcalSince, gcalUntil)
				if err != nil {
					return fmt.Errorf("fetch calendar events: %w", err)
				}
				if len(events) > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "calendar: %d events\n", len(events))
				}

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
						Commits: []session.Commit{
							{
								Repo:      "📅 " + ev.Title,
								SHA:       "",
								Message:   ev.Title,
								Timestamp: ev.Start,
							},
						},
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

			fmt.Fprintf(cmd.OutOrStdout(), "total: %d commits\n", total)
			for _, day := range days {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %dh (%d sessions)\n", day.Day, day.TotalHours, len(day.Sessions))
				for i, sess := range day.Sessions {
					repos := getUniqueReposForSession(sess)
					repoStr := strings.Join(repos, ", ")
					fmt.Fprintf(cmd.OutOrStdout(), "  session %d: %dh [%s]\n", i+1, sess.FuzzyHours, repoStr)
					if showCommits {
						commitIdx := 0
						for j := 0; j < len(sess.Commits); {
							c := sess.Commits[j]
							// Truncate message to first line and limit length
							msg := strings.SplitN(c.Message, "\n", 2)[0]
							if len(msg) > 60 {
								msg = msg[:60] + "..."
							}

							// Count consecutive duplicates (same message, different SHA)
							dupCount := 1
							for k := j + 1; k < len(sess.Commits); k++ {
								nextMsg := strings.SplitN(sess.Commits[k].Message, "\n", 2)[0]
								if len(nextMsg) > 60 {
									nextMsg = nextMsg[:60] + "..."
								}
								if nextMsg == msg {
									dupCount++
								} else {
									break
								}
							}

							commitIdx++
							if dupCount > 1 {
								fmt.Fprintf(cmd.OutOrStdout(), "    commit %d: %s %s (x%d)\n", commitIdx, c.SHA[:8], msg, dupCount)
								j += dupCount
							} else {
								fmt.Fprintf(cmd.OutOrStdout(), "    commit %d: %s %s\n", commitIdx, c.SHA[:8], msg)
								j++
							}
						}
					}
				}
			}

			// Print month summaries
			months := calculateMonthSummaries(days)
			if len(months) > 0 {
				fmt.Fprintln(cmd.OutOrStdout())
				for _, m := range months {
					days := float64(m.TotalHours) / 8.0
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %dh (%.1f days)\n", m.Month, m.TotalHours, days)
				}
			}

			// Print grand total
			grandTotal := 0
			for _, day := range days {
				grandTotal += day.TotalHours
			}
			fmt.Fprintln(cmd.OutOrStdout())
			grandTotalDays := float64(grandTotal) / 8.0
			fmt.Fprintf(cmd.OutOrStdout(), "grand total: %dh (%.1f days)\n", grandTotal, grandTotalDays)

			if pushNoko || nokoDryRun {
				nokoToken := strings.TrimSpace(nokoTokenInput)
				if nokoToken == "" {
					nokoToken = strings.TrimSpace(os.Getenv("NOKO_TOKEN"))
				}
				if nokoToken == "" && cfg.Noko != nil {
					nokoToken = strings.TrimSpace(cfg.Noko.APIToken)
				}
				if nokoToken == "" && !nokoDryRun {
					return fmt.Errorf("--push-noko requires a Noko API token; set --noko-token, NOKO_TOKEN, or noko.api_token in config")
				}

				var nokoClient *noko.Client
				if !nokoDryRun {
					nokoClient = noko.NewClient(nokoToken, nil)
				}

				if nokoDryRun {
					fmt.Fprintln(cmd.OutOrStdout())
					fmt.Fprintln(cmd.OutOrStdout(), "─── Noko dry run ──────────────────────────────────────────")
				}

				var llmClient *llm.Client
				if llmSummarize {
					llmCfg := resolveLLMConfig(cfg, llmAPIKey, llmModel)
					if llmCfg.APIKey != "" {
						llmClient = llm.NewClient(llmCfg)
					}
				}

				pushed := 0
				for _, day := range days {
					for _, sess := range day.Sessions {
						repos := getUniqueReposForSession(sess)
						groups := groupByProject(repos, cfg.Noko)
						minutesPerGroup := sess.FuzzyHours * 60 / len(groups)
						if minutesPerGroup < 1 {
							minutesPerGroup = 1
						}
						effectiveMin := minLogMinutes
						if effectiveMin == 0 && cfg.Noko != nil && cfg.Noko.MinLogMinutes > 0 {
							effectiveMin = cfg.Noko.MinLogMinutes
						}
						if effectiveMin > 0 && minutesPerGroup < effectiveMin {
							minutesPerGroup = effectiveMin
						}
						for projID, projRepos := range groups {
							if projID == 0 {
								if nokoDryRun {
									desc := strings.Join(projRepos, ", ")
									fmt.Fprintf(cmd.OutOrStdout(), "  %s  %dm  project=%d  %s  (skipped — no project mapping)\n", day.Day, minutesPerGroup, projID, desc)
								}
								continue
							}
							desc := strings.Join(projRepos, ", ")
							if len(sess.Commits) > 0 {
								desc += fmt.Sprintf(" (%d commits)", len(sess.Commits))
							}

							summary := sessionSummary(sess, projRepos, llmClient)
							if summary != "" {
								desc += ": " + summary
							}

							if nokoDryRun {
								fmt.Fprintf(cmd.OutOrStdout(), "  %s  %dm  project=%d  %s\n", day.Day, minutesPerGroup, projID, desc)
							} else {
								entry := noko.EntryRequest{
									Date:        day.Day,
									Minutes:     minutesPerGroup,
									Description: desc,
									ProjectID:   projID,
								}
								if err := nokoClient.CreateEntry(context.Background(), entry); err != nil {
									return fmt.Errorf("push session %s %s: %w", day.Day, desc, err)
								}
							}
							pushed++
						}
					}
				}
				label := "pushed"
				if nokoDryRun {
					label = "would push"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s %d sessions to Noko\n", label, pushed)
			}

			// Print flag summary
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "flags used:")
			if sinceInput != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  --since %s\n", sinceInput)
			}
			if untilInput != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  --until %s\n", untilInput)
			}
			if meOnly {
				fmt.Fprintln(cmd.OutOrStdout(), "  --me")
			}
			if authorFilter != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  --author=%s\n", authorFilter)
			}
			if sessionGapInput != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  --session-gap %s\n", sessionGapInput)
			}
			if timezoneInput != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  --timezone %s\n", timezoneInput)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "  --local")
			if len(localPaths) > 0 {
				for _, p := range localPaths {
					fmt.Fprintf(cmd.OutOrStdout(), "  --local-path %s\n", p)
				}
			}
			if discoverSubmodules {
				fmt.Fprintln(cmd.OutOrStdout(), "  --discover-submodules")
				fmt.Fprintf(cmd.OutOrStdout(), "  --max-depth %d\n", maxDepth)
			}
			if showCommits {
				fmt.Fprintln(cmd.OutOrStdout(), "  --show-commits")
			}
			if pushNoko {
				fmt.Fprintln(cmd.OutOrStdout(), "  --push-noko")
			}
			if nokoDryRun {
				fmt.Fprintln(cmd.OutOrStdout(), "  --noko-dry-run")
			}
			if nokoTokenInput != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  --noko-token %s\n", nokoTokenInput)
			}
			if minLogMinutes > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "  --min-log-minutes %d\n", minLogMinutes)
			}
			if llmSummarize {
				fmt.Fprintln(cmd.OutOrStdout(), "  --llm-summarize")
			}
			if gcalFlag {
				fmt.Fprintln(cmd.OutOrStdout(), "  --gcal")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&sinceInput, "since", "", "Start date/time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVar(&untilInput, "until", "", "End date/time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token (defaults to GITHUB_TOKEN or GH_TOKEN)")
	cmd.Flags().BoolVar(&meOnly, "me", false, "Only include commits authored by the authenticated GitHub user")
	cmd.Flags().StringVar(&authorFilter, "author", "", "Only include commits by the given GitHub login (alternative to --me for CI)")
	cmd.Flags().StringVar(&sessionGapInput, "session-gap", "", "Session split gap override (e.g. 2h, 90m)")
	cmd.Flags().StringVar(&timezoneInput, "timezone", "", "IANA timezone for day bucketing (e.g. America/New_York)")
	cmd.Flags().BoolVar(&localMode, "local", true, "Use local git history instead of GitHub API")
	cmd.Flags().StringSliceVar(&localPaths, "local-path", nil, "Local git repo path(s) to scan (used with --local)")
	cmd.Flags().BoolVar(&discoverSubmodules, "discover-submodules", false, "Automatically discover git repositories in subdirectories")
	cmd.Flags().BoolVar(&showCommits, "show-commits", false, "Show individual commits within each session")
	cmd.Flags().BoolVar(&pushNoko, "push-noko", false, "Push session summaries to Noko")
	cmd.Flags().BoolVar(&nokoDryRun, "noko-dry-run", false, "Print what would be pushed to Noko without actually posting")
	cmd.Flags().StringVar(&nokoTokenInput, "noko-token", "", "Noko API token (defaults to NOKO_TOKEN env var or noko.api_token in config)")
	cmd.Flags().IntVar(&minLogMinutes, "min-log-minutes", 0, "Round per-project session minutes up to this minimum (default 0 = no rounding)")
	cmd.Flags().BoolVar(&llmSummarize, "llm-summarize", false, "Use LLM to summarize sessions instead of commit message splicing")
	cmd.Flags().StringVar(&llmAPIKey, "llm-api-key", "", "OpenAI API key (defaults to LLM_API_KEY env var or llm.api_key in config)")
	cmd.Flags().StringVar(&llmModel, "llm-model", "", "LLM model (defaults to config or gpt-4o-mini)")
	cmd.Flags().BoolVar(&gcalFlag, "gcal", false, "Include calendar events from gcalcli as work sessions")
	cmd.Flags().IntVar(&maxDepth, "max-depth", 3, "Maximum depth for submodule discovery (default: 3)")
	cmd.Flags().StringSliceVar(&reposInput, "repos", nil, "Repositories to scan (owner/repo format, overrides config file)")

	return cmd
}

func normalizeCommit(repo string, c *gh.RepositoryCommit) (session.Commit, bool) {
	if c == nil || c.Commit == nil {
		return session.Commit{}, false
	}

	var timestamp time.Time
	if c.Commit.Author != nil {
		timestamp = c.Commit.Author.GetDate().Time
	}
	if timestamp.IsZero() && c.Commit.Committer != nil {
		timestamp = c.Commit.Committer.GetDate().Time
	}
	if timestamp.IsZero() {
		return session.Commit{}, false
	}

	return session.Commit{
		Repo:      repo,
		SHA:       c.GetSHA(),
		Message:   c.Commit.GetMessage(),
		Timestamp: timestamp,
	}, true
}

func commitMatchesViewer(c *gh.RepositoryCommit, viewer *ghclient.ViewerIdentity) bool {
	if c == nil || viewer == nil {
		return false
	}
	if c.Author != nil && strings.EqualFold(c.Author.GetLogin(), viewer.Login) {
		return true
	}
	if c.Committer != nil && strings.EqualFold(c.Committer.GetLogin(), viewer.Login) {
		return true
	}

	// Fallback for commits where API user linkage is missing.
	if c.Author == nil && c.Commit != nil && c.Commit.Author != nil {
		if _, ok := viewer.Emails[strings.ToLower(strings.TrimSpace(c.Commit.Author.GetEmail()))]; ok {
			return true
		}
	}
	if c.Committer == nil && c.Commit != nil && c.Commit.Committer != nil {
		if _, ok := viewer.Emails[strings.ToLower(strings.TrimSpace(c.Commit.Committer.GetEmail()))]; ok {
			return true
		}
	}
	return false
}

func getUniqueReposForSession(s session.Session) []string {
	repoSet := make(map[string]struct{})
	for _, c := range s.Commits {
		repoSet[c.Repo] = struct{}{}
	}
	repos := make([]string, 0, len(repoSet))
	for repo := range repoSet {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	return repos
}

// groupByProject groups repos by their Noko project ID.
// Repos without a project mapping fall into project 0.
func groupByProject(repos []string, nc *config.NokoConfig) map[int][]string {
	groups := make(map[int][]string)
	for _, r := range repos {
		projID, _ := nc.ProjectForRepo(r)
		groups[projID] = append(groups[projID], r)
	}
	return groups
}

// summarizeSession extracts a short summary of unique commit messages
// for commits belonging to the given repos, limited to 3 items.
func summarizeSession(sess session.Session, repos []string) string {
	repoSet := make(map[string]struct{}, len(repos))
	for _, r := range repos {
		repoSet[r] = struct{}{}
	}

	seen := make(map[string]struct{})
	msgs := make([]string, 0, 3)
	for _, c := range sess.Commits {
		if _, ok := repoSet[c.Repo]; !ok {
			continue
		}
		msg := strings.SplitN(c.Message, "\n", 2)[0]
		msg = strings.TrimSpace(msg)
		if msg == "" {
			continue
		}
		if _, ok := seen[msg]; ok {
			continue
		}
		seen[msg] = struct{}{}
		if len(msg) > 55 {
			msg = msg[:55] + "..."
		}
		msgs = append(msgs, msg)
		if len(msgs) >= 3 {
			break
		}
	}
	return strings.Join(msgs, "; ")
}

func calculateMonthSummaries(days []session.DaySummary) []struct {
	Month      string
	TotalHours int
} {
	monthMap := make(map[string]int)
	for _, day := range days {
		// Parse the day string (format: "2006-01-02")
		if len(day.Day) >= 7 {
			monthKey := day.Day[:7] // "2006-01"
			monthMap[monthKey] += day.TotalHours
		}
	}

	// Convert to slice and sort by month
	months := make([]struct {
		Month      string
		TotalHours int
	}, 0, len(monthMap))
	for month, hours := range monthMap {
		months = append(months, struct {
			Month      string
			TotalHours int
		}{Month: month, TotalHours: hours})
	}

	sort.Slice(months, func(i, j int) bool {
		return months[i].Month < months[j].Month
	})

	return months
}

func repoLabelForPath(path string) string {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return "."
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return clean
	}
	return filepath.Base(abs)
}

func localCommitMatchesMe(c localgit.Commit, emails map[string]struct{}) bool {
	if _, ok := emails[strings.ToLower(strings.TrimSpace(c.AuthorEmail))]; ok {
		return true
	}
	if _, ok := emails[strings.ToLower(strings.TrimSpace(c.CommitterEmail))]; ok {
		return true
	}
	return false
}

func discoverGitRepositories(rootPaths []string, maxDepth int) ([]string, error) {
	results := make([]string, 0)
	seen := make(map[string]struct{})

	var walkDir func(path string, depth int) error
	walkDir = func(path string, depth int) error {
		if depth > maxDepth {
			return nil
		}

		entries, err := os.ReadDir(path)
		if err != nil {
			// Skip directories we can't read (e.g., permission denied)
			if os.IsPermission(err) {
				return nil
			}
			return err
		}

		for _, entry := range entries {
			// Skip hidden directories
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}

			fullPath := filepath.Join(path, entry.Name())

			if entry.IsDir() {
				// Check if this directory is a git repository
				gitDir := filepath.Join(fullPath, ".git")
				if _, err := os.Stat(gitDir); err == nil {
					absPath, err := filepath.Abs(fullPath)
					if err != nil {
						continue
					}
					if _, exists := seen[absPath]; !exists {
						seen[absPath] = struct{}{}
						results = append(results, fullPath)
					}
					// Don't recurse into git repositories we've found
					continue
				}

				// Recurse into subdirectory
				if err := walkDir(fullPath, depth+1); err != nil {
					// Continue even if one subdirectory fails
					continue
				}
			}
		}

		return nil
	}

	for _, rootPath := range rootPaths {
		absRoot, err := filepath.Abs(rootPath)
		if err != nil {
			continue
		}
		// Check if root itself is a git repository and add it if not already in results
		gitDir := filepath.Join(absRoot, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			if _, exists := seen[absRoot]; !exists {
				seen[absRoot] = struct{}{}
				results = append(results, rootPath)
			}
		}
		// Walk the directory
		if err := walkDir(absRoot, 0); err != nil {
			// Continue with other paths even if one fails
			continue
		}
	}

	return results, nil
}

func splitRepo(repo string) (string, string, error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo %q: expected owner/repo", repo)
	}
	return parts[0], parts[1], nil
}

func parseDateBound(input string, endOfDay bool) (*time.Time, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return nil, nil
	}

	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return &parsed, nil
	}

	day, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil, fmt.Errorf("must be RFC3339 or YYYY-MM-DD")
	}
	if endOfDay {
		day = day.Add(23*time.Hour + 59*time.Minute + 59*time.Second)
	}
	return &day, nil
}

func resolveLLMConfig(cfg *config.RuntimeConfig, flagKey string, flagModel string) llm.Config {
	llmCfg := llm.Config{}

	if flagKey != "" {
		llmCfg.APIKey = flagKey
	} else if k := strings.TrimSpace(os.Getenv("LLM_API_KEY")); k != "" {
		llmCfg.APIKey = k
	} else if cfg.LLM != nil {
		llmCfg.APIKey = cfg.LLM.APIKey
	}

	if flagModel != "" {
		llmCfg.Model = flagModel
	} else if cfg.LLM != nil && cfg.LLM.Model != "" {
		llmCfg.Model = cfg.LLM.Model
	}

	if cfg.LLM != nil && cfg.LLM.BaseURL != "" {
		llmCfg.BaseURL = cfg.LLM.BaseURL
	}

	return llmCfg
}

func sessionSummary(sess session.Session, repos []string, llmClient *llm.Client) string {
	repoSet := make(map[string]struct{}, len(repos))
	for _, r := range repos {
		repoSet[r] = struct{}{}
	}

	seen := make(map[string]struct{})
	msgs := make([]string, 0, 20)
	for _, c := range sess.Commits {
		if _, ok := repoSet[c.Repo]; !ok {
			continue
		}
		msg := strings.SplitN(c.Message, "\n", 2)[0]
		msg = strings.TrimSpace(msg)
		if msg == "" {
			continue
		}
		if _, ok := seen[msg]; ok {
			continue
		}
		seen[msg] = struct{}{}
		msgs = append(msgs, msg)
		if len(msgs) >= 20 {
			break
		}
	}

	if llmClient != nil {
		summary, err := llmClient.Summarize(msgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "LLM summarize: %v, falling back to commit splicing\n", err)
		}
		if err == nil && summary != "" {
			return summary
		}
	}

	if len(msgs) == 0 {
		return ""
	}
	if len(msgs) > 3 {
		msgs = msgs[:3]
	}
	for i, m := range msgs {
		if len(m) > 55 {
			msgs[i] = m[:55] + "..."
		}
	}
	return strings.Join(msgs, "; ")
}
