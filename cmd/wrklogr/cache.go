package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bean-la/wrklogr/internal/config"
	"github.com/spf13/cobra"
)

type cacheFile struct {
	Version     int                `json:"version"`
	Month       string             `json:"month"`
	Since       string             `json:"since"`
	Until       string             `json:"until"`
	Updated     string             `json:"updated"`
	Authors     []string           `json:"authors"`
	Days        []cachedDay        `json:"days"`
	NokoEntries []cachedNokoEntry  `json:"noko_entries"`
}

type cachedDay struct {
	Day      string          `json:"day"`
	Hours    int             `json:"hours"`
	Sessions []cachedSession `json:"sessions"`
}

type cachedSession struct {
	Hours int      `json:"hours"`
	Repos []string `json:"repos"`
}

type cachedNokoEntry struct {
	Date        string `json:"date"`
	Minutes     int    `json:"minutes"`
	ProjectID   int    `json:"project_id"`
	Description string `json:"description"`
}

func newCacheCmd(getConfig func() (*config.RuntimeConfig, error)) *cobra.Command {
	var orgsInput []string
	var authorsInput []string
	var sinceInput string
	var untilInput string
	var cacheDir string
	var gcalFlag bool
	var token string

	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Generate daily cache file for incremental monthly reports",
		Long: `Run the report and write daily session summaries as JSON to a cache file.
The cache file can be used by generate-wiki to skip already-processed days.`,
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

			authToken := resolveToken(token)
			if authToken == "" {
				return fmt.Errorf("GitHub token required (--token, GITHUB_TOKEN, or GH_TOKEN)")
			}

			repos, err := discoverReposFromOrgs(context.Background(), authToken, orgsInput)
			if err != nil {
				return fmt.Errorf("discover repos from orgs: %w", err)
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
			gcalCal, gcalAttendee := "", ""
			var gcalKeywords []string
			if cfg.GCal != nil {
				gcalCal = cfg.GCal.Calendar
				gcalAttendee = cfg.GCal.Attendee
				gcalKeywords = cfg.GCal.Keywords
			}

			if cacheDir == "" {
				cacheDir = ".wrklogr-cache"
			}

			var allDays []cachedDay
			var allNoko []cachedNokoEntry
			for _, author := range authorsInput {
				opts := reportOpts{
					Repos:         repos,
					Since:         since,
					Until:         until,
					Author:        author,
					SessionGap:    sessionGap,
					Timezone:      reportTZ,
					GCalFlag:      gcalFlag,
					GCalCalendar:  gcalCal,
					GCalAttendee:  gcalAttendee,
					GCalKeywords:  gcalKeywords,
					NokoDryRun:    true,
					NokoConfig:    nokoCfg,
					LLMConfig:     &llmCfg,
					GitHubToken:   authToken,
					MinLogMinutes: nokoMinLog(nokoCfg),
				}

				result, runErr := runReport(context.Background(), cmd.OutOrStdout(), opts)
				if runErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "report for %s: %v\n", author, runErr)
					continue
				}

				for _, day := range result.Days {
					cd := cachedDay{
						Day:   day.Day,
						Hours: day.TotalHours,
					}
					for _, s := range day.Sessions {
						if isCalendarSession(s) {
							continue
						}
						cd.Sessions = append(cd.Sessions, cachedSession{
							Hours: s.FuzzyHours,
							Repos: getUniqueReposForSession(s),
						})
					}
					allDays = append(allDays, cd)
				}

				for _, entry := range result.NokoEntries {
					allNoko = append(allNoko, cachedNokoEntry{
						Date:        entry.Date,
						Minutes:     entry.Minutes,
						ProjectID:   entry.ProjectID,
						Description: entry.Description,
					})
				}
			}

			// Merge days (multiple authors may contribute to same day)
			dayMap := make(map[string]*cachedDay)
			for i := range allDays {
				d := &allDays[i]
				if existing, ok := dayMap[d.Day]; ok {
					existing.Hours += d.Hours
					existing.Sessions = append(existing.Sessions, d.Sessions...)
				} else {
					dayMap[d.Day] = d
				}
			}

			var mergedDays []cachedDay
			for _, d := range dayMap {
				mergedDays = append(mergedDays, *d)
			}

			cf := cacheFile{
				Version:     1,
				Month:       since.Format("2006-01"),
				Since:       since.Format("2006-01-02"),
				Until:       until.Format("2006-01-02"),
				Updated:     time.Now().UTC().Format(time.RFC3339),
				Authors:     authorsInput,
				Days:        mergedDays,
				NokoEntries: allNoko,
			}

			if err := os.MkdirAll(cacheDir, 0755); err != nil {
				return fmt.Errorf("create cache dir: %w", err)
			}

			monthFile := filepath.Join(cacheDir, cf.Month+".json")
			data, err := json.MarshalIndent(cf, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal cache: %w", err)
			}
			if err := os.WriteFile(monthFile, data, 0644); err != nil {
				return fmt.Errorf("write cache: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d days)\n", monthFile, len(mergedDays))
			return nil
		},
	}

	cmd.Flags().StringSliceVar(&orgsInput, "orgs", nil, "GitHub orgs to scan")
	cmd.Flags().StringSliceVar(&authorsInput, "authors", nil, "GitHub logins to track")
	cmd.Flags().StringVar(&sinceInput, "since", "", "Start date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&untilInput, "until", "", "End date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&cacheDir, "cache-dir", "", "Cache directory (default .wrklogr-cache)")
	cmd.Flags().BoolVar(&gcalFlag, "gcal", false, "Include Google Calendar events")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token")

	return cmd
}
