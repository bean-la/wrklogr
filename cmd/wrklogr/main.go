package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bean/wrklogr/internal/config"
	ghclient "github.com/bean/wrklogr/internal/github"
	"github.com/bean/wrklogr/internal/localgit"
	"github.com/bean/wrklogr/internal/session"
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
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Name() == "version" {
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
	root.AddCommand(newVersionCmd())
	root.AddCommand(newReportCmd(func() (*config.RuntimeConfig, error) {
		if cfg == nil {
			return nil, fmt.Errorf("config is not loaded")
		}
		return cfg, nil
	}))
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
	var sessionGapInput string
	var timezoneInput string
	var localMode bool
	var localPaths []string

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Fetch commits for configured repositories",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := getConfig()
			if err != nil {
				return err
			}
			if len(cfg.Repos) == 0 {
				return fmt.Errorf("no repositories configured; set repos in wrklogr.toml")
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
				client := ghclient.NewClient(authToken, nil)
				ctx := context.Background()

				var viewer *ghclient.ViewerIdentity
				if meOnly {
					identity, identityErr := client.GetViewerIdentity(ctx)
					if identityErr != nil {
						return fmt.Errorf("resolve --me identity: %w", identityErr)
					}
					viewer = identity
				}

				for _, fullRepo := range cfg.Repos {
					owner, repo, parseErr := splitRepo(fullRepo)
					if parseErr != nil {
						return parseErr
					}

					commits, fetchErr := client.ListCommits(ctx, owner, repo, since, until)
					if fetchErr != nil {
						return fetchErr
					}
					filtered := 0
					for _, c := range commits {
						if meOnly && !commitMatchesViewer(c, viewer) {
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

			fmt.Fprintf(cmd.OutOrStdout(), "total: %d commits\n", total)
			for _, day := range days {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %dh (%d sessions)\n", day.Day, day.TotalHours, len(day.Sessions))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&sinceInput, "since", "", "Start date/time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVar(&untilInput, "until", "", "End date/time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token (defaults to GITHUB_TOKEN or GH_TOKEN)")
	cmd.Flags().BoolVar(&meOnly, "me", false, "Filter commits to the authenticated GitHub user")
	cmd.Flags().StringVar(&sessionGapInput, "session-gap", "", "Session split gap override (e.g. 2h, 90m)")
	cmd.Flags().StringVar(&timezoneInput, "timezone", "", "IANA timezone for day bucketing (e.g. America/New_York)")
	cmd.Flags().BoolVar(&localMode, "local", false, "Use local git history instead of GitHub API")
	cmd.Flags().StringSliceVar(&localPaths, "local-path", nil, "Local git repo path(s) to scan (used with --local)")

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

func localCommitMatchesMe(c localgit.Commit, emails map[string]struct{}) bool {
	if _, ok := emails[strings.ToLower(strings.TrimSpace(c.AuthorEmail))]; ok {
		return true
	}
	if _, ok := emails[strings.ToLower(strings.TrimSpace(c.CommitterEmail))]; ok {
		return true
	}
	return false
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
