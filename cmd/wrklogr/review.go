package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/bean-la/wrklogr/internal/config"
	gcal "github.com/bean-la/wrklogr/internal/gcal"
	ghclient "github.com/bean-la/wrklogr/internal/github"
	"github.com/bean-la/wrklogr/internal/llm"
	noko "github.com/bean-la/wrklogr/internal/noko"
	"github.com/bean-la/wrklogr/internal/session"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

func newReviewCmd(getConfig func() (*config.RuntimeConfig, error)) *cobra.Command {
	var month string
	var sessionGapInput string
	var timezoneInput string

	cmd := &cobra.Command{
		Use:   "review",
		Short: "Interactive review and push workflow for a month",
		Long: `Review and push a month's worklog to Noko.
Runs the report, shows unassigned calendar events, lets you assign
them, shows a dry-run, then pushes to Noko on confirmation.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := getConfig()
			if err != nil {
				if !os.IsNotExist(err) {
					return err
				}
				cfg = &config.RuntimeConfig{
					SessionGap: 2 * time.Hour,
					Timezone:   time.UTC,
				}
			}

			since, until, err := parseMonth(month)
			if err != nil {
				return err
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

			authToken := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
			if authToken == "" {
				authToken = strings.TrimSpace(os.Getenv("GH_TOKEN"))
			}
			if authToken == "" {
				if tok, err := exec.Command("gh", "auth", "token").Output(); err == nil {
					authToken = strings.TrimSpace(string(tok))
				}
			}

			// --- Fetch commits ---
			repos := cfg.Repos
			if len(repos) == 0 {
				return fmt.Errorf("no repositories configured; set repos in wrklogr.toml")
			}

			merged := make([]session.Commit, 0, 128)
			total := 0

			if authToken != "" {
				client := ghclient.NewClient(authToken, nil)
				ctx := context.Background()

				for _, fullRepo := range repos {
					owner, repoName, parseErr := splitRepo(fullRepo)
					if parseErr != nil {
						return parseErr
					}
					commits, fetchErr := client.ListCommits(ctx, owner, repoName, since, until)
					if fetchErr != nil {
						fmt.Fprintf(cmd.OutOrStdout(), "%s: error — %v\n", fullRepo, fetchErr)
						continue
					}
					filtered := 0
					for _, c := range commits {
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
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "No GitHub token found; try local mode with --local")
				return fmt.Errorf("GitHub token required (set GITHUB_TOKEN, GH_TOKEN, or auth via gh CLI)")
			}

			sort.Slice(merged, func(i, j int) bool {
				return merged[i].Timestamp.Before(merged[j].Timestamp)
			})
			sessions := session.Build(merged, sessionGap)
			days := session.BucketByDay(sessions, reportTZ)

			// --- Fetch GCal events ---
			gcalCal := "seb@bean.la"
			if cfg.GCal != nil && cfg.GCal.Calendar != "" {
				gcalCal = cfg.GCal.Calendar
			}
			events, err := gcal.FetchEvents(gcalCal, *since, *until)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "calendar: error — %v\n", err)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "calendar: %d events\n", len(events))
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

			// --- Display summary ---
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintf(cmd.OutOrStdout(), "total: %d commits + %d calendar events\n", total, len(events))
			for _, day := range days {
				sessSummary := make([]string, 0, len(day.Sessions))
				for _, s := range day.Sessions {
					repos := getUniqueReposForSession(s)
					sessSummary = append(sessSummary, fmt.Sprintf("%dh [%s]", s.FuzzyHours, strings.Join(repos, ", ")))
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %dh (%s)\n", day.Day, day.TotalHours, strings.Join(sessSummary, ", "))
			}

			// --- Identify unassigned calendar events ---
			unassigned := findUnassignedEvents(days, cfg.Noko)

			if len(unassigned) > 0 {
				fmt.Fprintln(cmd.OutOrStdout())
				fmt.Fprintf(cmd.OutOrStdout(), "─── %d unassigned calendar events ───\n", len(unassigned))
				choices := listProjectOptions(cfg.Noko)

				for i, ev := range unassigned {
					fmt.Fprintf(cmd.OutOrStdout(), "\n%d. %s (%dh)\n", i+1, ev.Title, ev.FuzzyHours)
					fmt.Fprintf(cmd.OutOrStdout(), "   Select project [1-%d, 0=skip]: ", len(choices))
					choice := readInt(len(choices))
					if choice > 0 && choice <= len(choices) {
						selected := choices[choice-1]
						repoKey := "📅 " + ev.Title
						if cfg.Noko.RepoProjects == nil {
							cfg.Noko.RepoProjects = make(map[string]config.RepoProject)
						}
						cfg.Noko.RepoProjects[repoKey] = config.RepoProject{ProjectID: selected.ID}
						fmt.Fprintf(cmd.OutOrStdout(),("   → Assigned to project %d (%s)\n"), selected.ID, selected.Name)
					} else {
						fmt.Fprintln(cmd.OutOrStdout(), "   → Skipped")
					}
				}

				// Save config with new mappings
				if err := saveUpdatedConfig(cfg.Path, cfg.Noko); err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "warning: could not save config: %v\n", err)
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "\n✓ Project mappings saved to config")
				}
			}

			// --- Noko dry run ---
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "─── Noko dry run ──────────────────────────────────────────")

			var llmClient *llm.Client
			llmCfg := resolveLLMConfig(cfg, "", "")
			if llmCfg.APIKey != "" {
				llmClient = llm.NewClient(llmCfg)
			}

			nokoToken := strings.TrimSpace(os.Getenv("NOKO_TOKEN"))
			if nokoToken == "" && cfg.Noko != nil {
				nokoToken = strings.TrimSpace(cfg.Noko.APIToken)
			}
			if nokoToken == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "No Noko token found (set NOKO_TOKEN env var). Skipping dry run.")
				return nil
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
					effectiveMin := 0
					if cfg.Noko != nil && cfg.Noko.MinLogMinutes > 0 {
						effectiveMin = cfg.Noko.MinLogMinutes
					}
					if effectiveMin > 0 && minutesPerGroup < effectiveMin {
						minutesPerGroup = effectiveMin
					}
					for projID, projRepos := range groups {
						if projID == 0 {
							desc := strings.Join(projRepos, ", ")
							fmt.Fprintf(cmd.OutOrStdout(), "  %s  %dm  project=%d  %s  (skipped)\n", day.Day, minutesPerGroup, projID, desc)
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
						fmt.Fprintf(cmd.OutOrStdout(), "  %s  %dm  project=%d  %s\n", day.Day, minutesPerGroup, projID, desc)
						pushed++
					}
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "would push %d sessions to Noko\n", pushed)

			// --- Confirm push ---
			if pushed == 0 {
				return nil
			}

			fmt.Fprint(cmd.OutOrStdout(), "\nPush to Noko? [y/N]: ")
			answer := strings.TrimSpace(readLine())
			if strings.ToLower(answer) != "y" && strings.ToLower(answer) != "yes" {
				fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
				return nil
			}

			nokoClient := noko.NewClient(nokoToken, nil)
			pushedActual := 0
			for _, day := range days {
				for _, sess := range day.Sessions {
					repos := getUniqueReposForSession(sess)
					groups := groupByProject(repos, cfg.Noko)
					minutesPerGroup := sess.FuzzyHours * 60 / len(groups)
					if minutesPerGroup < 1 {
						minutesPerGroup = 1
					}
					effectiveMin := 0
					if cfg.Noko != nil && cfg.Noko.MinLogMinutes > 0 {
						effectiveMin = cfg.Noko.MinLogMinutes
					}
					if effectiveMin > 0 && minutesPerGroup < effectiveMin {
						minutesPerGroup = effectiveMin
					}
					for projID, projRepos := range groups {
						if projID == 0 {
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
						entry := noko.EntryRequest{
							Date:        day.Day,
							Minutes:     minutesPerGroup,
							Description: desc,
							ProjectID:   projID,
						}
						if err := nokoClient.CreateEntry(context.Background(), entry); err != nil {
							return fmt.Errorf("push session %s: %w", day.Day, err)
						}
						pushedActual++
					}
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ Pushed %d sessions to Noko\n", pushedActual)
			return nil
		},
	}

	cmd.Flags().StringVar(&month, "month", "", "Month to review (YYYY-MM format, e.g. 2026-04)")
	cmd.Flags().StringVar(&sessionGapInput, "session-gap", "", "Session split gap override (e.g. 2h, 90m)")
	cmd.Flags().StringVar(&timezoneInput, "timezone", "", "IANA timezone for day bucketing")

	return cmd
}

func parseMonth(month string) (since, until *time.Time, err error) {
	if month == "" {
		now := time.Now()
		month = now.Format("2006-01")
	}

	t, parseErr := time.Parse("2006-01", month)
	if parseErr != nil {
		return nil, nil, fmt.Errorf("invalid month %q: use YYYY-MM format", month)
	}

	s := t
	u := t.AddDate(0, 1, 0).Add(-time.Second)
	return &s, &u, nil
}

type unassignedEvent struct {
	Title      string
	FuzzyHours int
}

func findUnassignedEvents(days []session.DaySummary, nc *config.NokoConfig) []unassignedEvent {
	events := make([]unassignedEvent, 0)
	for _, day := range days {
		for _, sess := range day.Sessions {
			repos := getUniqueReposForSession(sess)
			for _, r := range repos {
				if strings.HasPrefix(r, "📅") {
					projID, _ := nc.ProjectForRepo(r)
					if projID == 0 {
						title := strings.TrimPrefix(r, "📅 ")
						events = append(events, unassignedEvent{Title: title, FuzzyHours: sess.FuzzyHours})
					}
				}
			}
		}
	}
	return events
}

type projectOption struct {
	ID   int
	Name string
}

func listProjectOptions(nc *config.NokoConfig) []projectOption {
	ids := make(map[int]string)

	// Collect from per-repo mappings
	if nc.RepoProjects != nil {
		for _, rp := range nc.RepoProjects {
			ids[rp.ProjectID] = ""
		}
	}

	ids[687237] = "Bean"
	ids[708823] = "Third Eye"
	ids[716638] = "Dear Freeda (TMV)"
	ids[586501] = "Dublab"
	ids[611157] = "Salon 94"
	ids[560046] = "Jono Pandolfi"
	ids[607240] = "Farm To People"
	ids[557928] = "Culinistas"
	ids[595328] = "Minisocial"
	ids[615238] = "Tripoli Gallery"
	ids[606165] = "Max Levai"
	ids[662679] = "Ghia"
	ids[639789] = "Syng"
	ids[590606] = "Tartine"

	opts := make([]projectOption, 0, len(ids))
	for id, name := range ids {
		if name == "" {
			name = fmt.Sprintf("Project %d", id)
		}
		opts = append(opts, projectOption{ID: id, Name: fmt.Sprintf("%d — %s", id, name)})
	}
	sort.Slice(opts, func(i, j int) bool { return opts[i].ID < opts[j].ID })
	return opts
}

func readInt(max int) int {
	var n int
	_, _ = fmt.Scanf("%d", &n)
	return n
}

func readLine() string {
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		return scanner.Text()
	}
	return ""
}

func saveUpdatedConfig(path string, nc *config.NokoConfig) error {
	if path == "" {
		return fmt.Errorf("config path unknown")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var cfg config.Config
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	if cfg.Noko == nil {
		cfg.Noko = nc
	} else {
		cfg.Noko.RepoProjects = nc.RepoProjects
	}

	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}
