package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

func newOnboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "onboard",
		Short: "Interactively create a wrklogr.toml config file",
		Long: `Guided wizard that creates wrklogr.toml in the current directory.
Auto-detects git repos from remotes, GitHub auth status, and system
timezone, then prompts for the remaining settings.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOnboard(cmd)
		},
	}
}

type onboardConfig struct {
	SessionGap string            `toml:"session_gap,omitempty"`
	Timezone   string            `toml:"timezone,omitempty"`
	Repos      []string          `toml:"repos,omitempty"`
	Noko       *onboardNoko      `toml:"noko,omitempty"`
	GCal       *onboardGCal      `toml:"gcal,omitempty"`
}

type onboardNoko struct {
	APIToken string `toml:"api_token,omitempty"`
}

type onboardGCal struct {
	Calendar string `toml:"calendar,omitempty"`
}

func runOnboard(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()

	if _, err := os.Stat("wrklogr.toml"); err == nil {
		fmt.Fprint(out, "wrklogr.toml already exists. Overwrite? [y/N]: ")
		answer := strings.TrimSpace(readLine())
		if strings.ToLower(answer) != "y" && strings.ToLower(answer) != "yes" {
			fmt.Fprintln(out, "Cancelled.")
			return nil
		}
	}

	ghToken := ""
	if tok, err := exec.Command("gh", "auth", "token").Output(); err == nil {
		ghToken = strings.TrimSpace(string(tok))
	}

	suggestedRepos := detectGitRemotes(".")
	detectedTZ := detectTimezone()

	fmt.Fprintln(out, "─── wrklogr onboard ─────────────────────────────────────")
	fmt.Fprintln(out)

	fmt.Fprintln(out, "─── Repositories ──────────────────────────────────────────")
	if len(suggestedRepos) > 0 {
		fmt.Fprintf(out, "Detected remotes: %s\n", strings.Join(suggestedRepos, ", "))
	}
	fmt.Fprintln(out, "Enter GitHub repos (owner/repo, comma-separated):")
	fmt.Fprint(out, "> ")
	reposInput := readLine()
	repos := parseRepoList(reposInput, suggestedRepos)
	if len(repos) == 0 {
		return fmt.Errorf("at least one repository is required")
	}
	fmt.Fprintf(out, "  → %s\n", strings.Join(repos, ", "))

	fmt.Fprintln(out)
	fmt.Fprintln(out, "─── Settings ───────────────────────────────────────────────")
	fmt.Fprintf(out, "Session gap [2h]: ")
	sessionGap := readStringOrDefault("2h")
	fmt.Fprintf(out, "  → %s\n", sessionGap)

	fmt.Fprintf(out, "Timezone [%s]: ", detectedTZ)
	timezone := readStringOrDefault(detectedTZ)
	fmt.Fprintf(out, "  → %s\n", timezone)

	fmt.Fprintln(out)
	fmt.Fprintln(out, "─── Noko (optional) ────────────────────────────────────────")
	fmt.Fprintln(out, "Noko API token (or press Enter to skip):")
	fmt.Fprint(out, "> ")
	nokoToken := readLine()
	if nokoToken != "" {
		fmt.Fprintln(out, "  → token set")
	} else {
		fmt.Fprintln(out, "  → skipped (set NOKO_TOKEN env var later)")
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "─── Google Calendar (optional) ─────────────────────────────")
	fmt.Fprintln(out, "Calendar ID for iCal events (or press Enter to skip):")
	fmt.Fprint(out, "> ")
	gcalCalendar := readLine()
	if gcalCalendar != "" {
		fmt.Fprintf(out, "  → %s\n", gcalCalendar)
	} else {
		fmt.Fprintln(out, "  → skipped")
	}

	cfg := onboardConfig{
		Repos:      repos,
		SessionGap: sessionGap,
		Timezone:   timezone,
	}
	if nokoToken != "" {
		cfg.Noko = &onboardNoko{APIToken: nokoToken}
	}
	if gcalCalendar != "" {
		cfg.GCal = &onboardGCal{Calendar: gcalCalendar}
	}

	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile("wrklogr.toml", data, 0644); err != nil {
		return fmt.Errorf("write wrklogr.toml: %w", err)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "✓ wrklogr.toml created")

	fmt.Fprintln(out)
	fmt.Fprintln(out, "─── Next steps ────────────────────────────────────────────")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  1. Run a report:")
	fmt.Fprintln(out, "       wrklogr report")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  2. Push to Noko:")
	fmt.Fprintln(out, "       wrklogr review")
	fmt.Fprintln(out)
	if ghToken == "" {
		fmt.Fprintln(out, "  3. Authenticate with GitHub for remote repos:")
		fmt.Fprintln(out, "       gh auth login")
		fmt.Fprintln(out, "     Or set the GITHUB_TOKEN env var.")
	}
	fmt.Fprintln(out, "  4. See all commands:")
	fmt.Fprintln(out, "       wrklogr --help")

	return nil
}

func detectGitRemotes(path string) []string {
	cmd := exec.Command("git", "-C", path, "remote", "-v")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	seen := make(map[string]struct{})
	var repos []string
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		repo := parseGitHubRemote(parts[1])
		if repo != "" && strings.Contains(repo, "/") {
			if _, ok := seen[repo]; !ok {
				seen[repo] = struct{}{}
				repos = append(repos, repo)
			}
		}
	}
	sort.Strings(repos)
	return repos
}

func parseRepoList(input string, fallback []string) []string {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return fallback
	}

	parts := strings.Split(raw, ",")
	var repos []string
	for _, p := range parts {
		r := strings.TrimSpace(p)
		if r != "" {
			repos = append(repos, r)
		}
	}
	return repos
}

func readStringOrDefault(def string) string {
	input := strings.TrimSpace(readLine())
	if input == "" {
		return def
	}
	return input
}

func detectTimezone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}

	link, err := os.Readlink("/etc/localtime")
	if err != nil {
		return "UTC"
	}

	if idx := strings.Index(link, "zoneinfo/"); idx >= 0 {
		return link[idx+len("zoneinfo/"):]
	}

	return "UTC"
}
