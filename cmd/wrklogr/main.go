package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bean/wrklogr/internal/config"
	ghclient "github.com/bean/wrklogr/internal/github"
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

			authToken := strings.TrimSpace(token)
			if authToken == "" {
				authToken = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
			}
			if authToken == "" {
				authToken = strings.TrimSpace(os.Getenv("GH_TOKEN"))
			}

			client := ghclient.NewClient(authToken, nil)
			ctx := context.Background()

			total := 0
			for _, fullRepo := range cfg.Repos {
				owner, repo, parseErr := splitRepo(fullRepo)
				if parseErr != nil {
					return parseErr
				}

				commits, fetchErr := client.ListCommits(ctx, owner, repo, since, until)
				if fetchErr != nil {
					return fetchErr
				}
				total += len(commits)
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %d commits\n", fullRepo, len(commits))
			}

			fmt.Fprintf(cmd.OutOrStdout(), "total: %d commits\n", total)
			return nil
		},
	}

	cmd.Flags().StringVar(&sinceInput, "since", "", "Start date/time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVar(&untilInput, "until", "", "End date/time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token (defaults to GITHUB_TOKEN or GH_TOKEN)")

	return cmd
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
