package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/bean-la/wrklogr/internal/config"
	"github.com/bean-la/wrklogr/internal/llm"
	"github.com/bean-la/wrklogr/internal/localgit"
	"github.com/bean-la/wrklogr/internal/noko"
	"github.com/bean-la/wrklogr/internal/notion"
	"github.com/bean-la/wrklogr/internal/session"
	"github.com/spf13/cobra"
)

type projectAgg struct {
	Minutes   int
	Repos     map[string]struct{}
	Msgs      []string // commit first-lines (up to 20, for LLM input)
	msgSeen   map[string]struct{}
	NokoDescs []string // descriptions fetched from Noko entries
}

func (a *projectAgg) addSession(sess session.Session, repos []string) {
	repoSet := make(map[string]struct{}, len(repos))
	for _, r := range repos {
		repoSet[r] = struct{}{}
	}
	for _, c := range sess.Commits {
		if _, ok := repoSet[c.Repo]; !ok {
			continue
		}
		a.Repos[c.Repo] = struct{}{}
		if len(a.Msgs) < 20 {
			msg := strings.SplitN(c.Message, "\n", 2)[0]
			msg = strings.TrimSpace(msg)
			if msg != "" {
				if _, seen := a.msgSeen[msg]; !seen {
					a.msgSeen[msg] = struct{}{}
					a.Msgs = append(a.Msgs, msg)
				}
			}
		}
	}
}

func newNotionInvoiceCmd(getConfig func() (*config.RuntimeConfig, error)) *cobra.Command {
	var sinceInput string
	var untilInput string
	var invoiceNumber string
	var notionToken string
	var githubToken string
	var localPaths []string
	var dryRun bool
	var update bool
	var author string
	var repoPatterns []string

	cmd := &cobra.Command{
		Use:   "notion-invoice",
		Short: "Create a draft invoice page in Notion for the given date range",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := getConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if cfg.Notion == nil {
				return fmt.Errorf("notion config required: add [notion] section to wrklogr.toml")
			}
			if cfg.Noko == nil {
				return fmt.Errorf("noko config required for project mapping: add [noko] section to wrklogr.toml")
			}

			token := strings.TrimSpace(notionToken)
			if token == "" {
				token = strings.TrimSpace(os.Getenv("NOTION_TOKEN"))
			}
			if token == "" {
				token = cfg.Notion.APIToken
			}
			if token == "" {
				return fmt.Errorf("notion API token required: set --notion-token, NOTION_TOKEN, or notion.api_token in config")
			}

			since, until, err := resolveBillingRange(sinceInput, untilInput)
			if err != nil {
				return err
			}

			ctx := context.Background()
			var days []session.DaySummary

			if len(localPaths) > 0 {
				var merged []session.Commit
				for _, p := range localPaths {
					commits, scanErr := localgit.ListCommits(p, &since, &until)
					if scanErr != nil {
						return fmt.Errorf("git log %s: %w", p, scanErr)
					}
					label := filepath.Base(p)
					for _, c := range commits {
						if author != "" && !strings.EqualFold(c.AuthorName, author) {
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
							Repo:      label,
							SHA:       c.SHA,
							Message:   c.Subject,
							Timestamp: ts,
						})
					}
				}
				sort.Slice(merged, func(i, j int) bool {
					return merged[i].Timestamp.Before(merged[j].Timestamp)
				})
				days = session.BucketByDay(session.Build(merged, cfg.SessionGap), cfg.Timezone)
			} else {
				// GitHub API mode — uses repos from config, filtered by --author.
				ghTok := strings.TrimSpace(githubToken)
				if ghTok == "" {
					ghTok = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
				}
				if ghTok == "" {
					ghTok = strings.TrimSpace(os.Getenv("GH_TOKEN"))
				}
				if ghTok == "" {
					if out, ghErr := exec.Command("gh", "auth", "token").Output(); ghErr == nil {
						ghTok = strings.TrimSpace(string(out))
					}
				}
				llmCfg := llm.Config{}
				result, reportErr := runReport(ctx, cmd.OutOrStdout(), reportOpts{
					Repos:       cfg.Repos,
					Since:       &since,
					Until:       &until,
					Author:      author,
					SessionGap:  cfg.SessionGap,
					Timezone:    cfg.Timezone,
					NokoConfig:  cfg.Noko,
					GitHubToken: ghTok,
					LLMConfig:   &llmCfg,
				})
				if reportErr != nil {
					return fmt.Errorf("fetch commits: %w", reportErr)
				}
				days = result.Days
			}

			if len(repoPatterns) > 0 {
				days = filterDaysByRepo(days, repoPatterns)
			}

			aggs := aggregateByProject(days, cfg.Noko)
			if len(aggs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no sessions found for the given date range")
				return nil
			}

			billedFrom := since.Format("2006-01-02")
			billedTo := until.Format("2006-01-02")
			monthLabel := since.Format("January 2006")

			// Fetch Noko log descriptions for each project to enrich the invoice.
			nokoToken := strings.TrimSpace(os.Getenv("NOKO_TOKEN"))
			if nokoToken == "" && cfg.Noko != nil {
				nokoToken = strings.TrimSpace(cfg.Noko.APIToken)
			}
			if nokoToken != "" {
				projIDs := make([]int, 0, len(aggs))
				for id := range aggs {
					projIDs = append(projIDs, id)
				}
				nokoClient := noko.NewClient(nokoToken, nil)
				entries, nokoErr := nokoClient.ListEntries(ctx, billedFrom, billedTo, nil, projIDs)
				if nokoErr != nil {
					fmt.Fprintf(os.Stderr, "warn: fetch noko entries: %v\n", nokoErr)
				} else {
					for _, e := range entries {
						if agg, ok := aggs[e.ProjectID()]; ok && strings.TrimSpace(e.Description) != "" {
							agg.NokoDescs = append(agg.NokoDescs, strings.TrimSpace(e.Description))
						}
					}
				}
			}

			// Build LLM client if configured.
			var llmClient *llm.Client
			llmCfg := resolveLLMConfig(cfg, "", "")
			if llmCfg.APIKey != "" {
				llmClient = llm.NewClient(llmCfg)
			}

			nc := notion.NewClient(token, nil)

			role := cfg.Notion.Role
			if role == "" {
				role = "backend"
			}

			if update {
				return runUpdate(cmd, ctx, nc, cfg, invoiceNumber, role, aggs, billedFrom, billedTo, monthLabel, dryRun, llmClient)
			}
			return runCreate(cmd, ctx, nc, cfg, invoiceNumber, role, aggs, billedFrom, billedTo, monthLabel, dryRun, llmClient)
		},
	}

	cmd.Flags().StringVar(&author, "author", "", "Only include commits by this author (GitHub login for API mode, name for local mode)")
	cmd.Flags().StringSliceVar(&repoPatterns, "repo", nil, "Only include sessions touching repos matching these glob patterns (e.g. 'Third-Eye-Tarot/*')")
	cmd.Flags().StringVar(&githubToken, "token", "", "GitHub token for API mode (defaults to GITHUB_TOKEN, GH_TOKEN, or gh CLI)")
	cmd.Flags().BoolVar(&update, "update", false, "Update an existing invoice page instead of creating a new one (requires --invoice-number)")
	cmd.Flags().StringVar(&sinceInput, "since", "", "Start of billing period (YYYY-MM-DD, default: first of last month)")
	cmd.Flags().StringVar(&untilInput, "until", "", "End of billing period (YYYY-MM-DD, default: last of last month)")
	cmd.Flags().StringVar(&invoiceNumber, "invoice-number", "", "Invoice number (e.g. ADV-0200)")
	cmd.Flags().StringSliceVar(&localPaths, "local-path", nil, "Local git repo path(s) to scan")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would be created without writing to Notion")
	cmd.Flags().StringVar(&notionToken, "notion-token", "", "Notion API token (defaults to NOTION_TOKEN env or notion.api_token in config)")

	return cmd
}

// resolveBillingRange returns the since/until time.Time pair.
// If both inputs are empty, defaults to the previous calendar month.
func resolveBillingRange(sinceInput, untilInput string) (time.Time, time.Time, error) {
	if sinceInput != "" || untilInput != "" {
		var since, until time.Time
		var err error
		if sinceInput != "" {
			since, err = time.Parse("2006-01-02", sinceInput)
			if err != nil {
				return time.Time{}, time.Time{}, fmt.Errorf("parse --since: %w", err)
			}
		}
		if untilInput != "" {
			until, err = time.Parse("2006-01-02", untilInput)
			if err != nil {
				return time.Time{}, time.Time{}, fmt.Errorf("parse --until: %w", err)
			}
		}
		return since, until, nil
	}
	// Default: previous calendar month.
	now := time.Now().UTC()
	firstOfThisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	firstOfPrevMonth := firstOfThisMonth.AddDate(0, -1, 0)
	lastOfPrevMonth := firstOfThisMonth.Add(-time.Second)
	return firstOfPrevMonth, lastOfPrevMonth, nil
}

// filterDaysByRepo drops commits that don't match any of the given glob patterns,
// then drops sessions and days that become empty.
func filterDaysByRepo(days []session.DaySummary, patterns []string) []session.DaySummary {
	repoMatch := func(repo string) bool {
		for _, pat := range patterns {
			if ok, _ := path.Match(pat, repo); ok {
				return true
			}
		}
		return false
	}

	out := make([]session.DaySummary, 0, len(days))
	for _, day := range days {
		var sessions []session.Session
		for _, sess := range day.Sessions {
			var commits []session.Commit
			for _, c := range sess.Commits {
				if repoMatch(c.Repo) {
					commits = append(commits, c)
				}
			}
			if len(commits) > 0 {
				sess.Commits = commits
				sessions = append(sessions, sess)
			}
		}
		if len(sessions) > 0 {
			day.Sessions = sessions
			out = append(out, day)
		}
	}
	return out
}

// aggregateByProject totals minutes per Noko project ID across all sessions.
func aggregateByProject(days []session.DaySummary, nc *config.NokoConfig) map[int]*projectAgg {
	result := make(map[int]*projectAgg)
	for _, day := range days {
		for _, sess := range day.Sessions {
			repos := getUniqueReposForSession(sess)
			groups := groupByProject(repos, nc)
			minutesPerGroup := sess.FuzzyHours * 60 / len(groups)
			if minutesPerGroup < 1 {
				minutesPerGroup = 1
			}
			for projID, projRepos := range groups {
				if projID == 0 {
					continue
				}
				agg, ok := result[projID]
				if !ok {
					agg = &projectAgg{
						Repos:   make(map[string]struct{}),
						msgSeen: make(map[string]struct{}),
					}
					result[projID] = agg
				}
				agg.Minutes += minutesPerGroup
				agg.addSession(sess, projRepos)
			}
		}
	}
	return result
}

func runCreate(
	cmd *cobra.Command,
	ctx context.Context,
	nc *notion.Client,
	cfg *config.RuntimeConfig,
	invoiceNumber, role string,
	aggs map[int]*projectAgg,
	billedFrom, billedTo, monthLabel string,
	dryRun bool,
	llmClient *llm.Client,
) error {
	projIDs := make([]int, 0, len(aggs))
	for id := range aggs {
		projIDs = append(projIDs, id)
	}
	sort.Ints(projIDs)

	for _, projID := range projIDs {
		agg := aggs[projID]
		hours := float64(agg.Minutes) / 60.0

		client, err := nc.FindClientByNokoProjectID(ctx, cfg.Notion.ClientsDBID, projID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: notion lookup for project %d: %v\n", projID, err)
			continue
		}
		if client == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "warn: no Notion client for Noko project %d — skipping\n", projID)
			continue
		}

		rate := client.RateForRole(role)
		if rate == 0 {
			rate = cfg.Notion.DefaultRate
		}
		amount := hours * rate
		repoNames := sortedRepos(agg)
		desc := buildInvoiceDescription(monthLabel, repoNames, agg.NokoDescs, agg.Msgs, llmClient)

		inv := notion.InvoiceRequest{
			InvoiceNumber: invoiceNumber,
			Amount:        amount,
			BilledFrom:    billedFrom,
			BilledTo:      billedTo,
			ClientPageID:  client.PageID,
			Description:   desc,
			NET:           client.NET,
			NetDays:       client.NetDays,
		}

		if dryRun {
			printInvoiceSummary(cmd, client.Name, invoiceNumber, amount, hours, rate, role, billedFrom, billedTo, client.NET, client.NetDays, repoNames, agg.NokoDescs, desc)
			continue
		}

		pageID, err := nc.CreateInvoice(ctx, cfg.Notion.InvoiceDBID, inv)
		if err != nil {
			return fmt.Errorf("create invoice for %s: %w", client.Name, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "created invoice for %s: page=%s amount=$%.2f\n", client.Name, pageID, amount)
	}
	return nil
}

func runUpdate(
	cmd *cobra.Command,
	ctx context.Context,
	nc *notion.Client,
	cfg *config.RuntimeConfig,
	invoiceNumber, role string,
	aggs map[int]*projectAgg,
	billedFrom, billedTo, monthLabel string,
	dryRun bool,
	llmClient *llm.Client,
) error {
	if invoiceNumber == "" {
		return fmt.Errorf("--update requires --invoice-number")
	}

	existing, err := nc.FindInvoiceByNumber(ctx, cfg.Notion.InvoiceDBID, invoiceNumber)
	if err != nil {
		return fmt.Errorf("find invoice %s: %w", invoiceNumber, err)
	}
	if existing == nil {
		return fmt.Errorf("invoice %s not found in Notion", invoiceNumber)
	}

	// Resolve client from the invoice's existing relation.
	var client *notion.ClientRecord
	if existing.ClientPageID != "" {
		client, err = nc.GetClientPage(ctx, existing.ClientPageID)
		if err != nil {
			return fmt.Errorf("get client page for %s: %w", invoiceNumber, err)
		}
	}

	// Pick the aggregate that matches this client's Noko project.
	var agg *projectAgg
	if client != nil && client.NokoProjectID != 0 {
		agg = aggs[client.NokoProjectID]
	}
	if agg == nil {
		// Fall back: merge all projects into one.
		agg = &projectAgg{
			Repos:   make(map[string]struct{}),
			msgSeen: make(map[string]struct{}),
		}
		for _, a := range aggs {
			agg.Minutes += a.Minutes
			for r := range a.Repos {
				agg.Repos[r] = struct{}{}
			}
			for _, m := range a.Msgs {
				if _, seen := agg.msgSeen[m]; !seen {
					agg.msgSeen[m] = struct{}{}
					agg.Msgs = append(agg.Msgs, m)
				}
			}
			agg.NokoDescs = append(agg.NokoDescs, a.NokoDescs...)
		}
	}

	hours := float64(agg.Minutes) / 60.0
	rate := 0.0
	clientName := invoiceNumber
	if client != nil {
		rate = client.RateForRole(role)
		clientName = client.Name
	}
	if rate == 0 {
		rate = cfg.Notion.DefaultRate
	}
	amount := hours * rate
	repoNames := sortedRepos(agg)
	desc := buildInvoiceDescription(monthLabel, repoNames, agg.NokoDescs, agg.Msgs, llmClient)

	inv := notion.InvoiceRequest{
		Amount:      amount,
		BilledFrom:  billedFrom,
		BilledTo:    billedTo,
		Description: desc,
	}
	if client != nil {
		inv.NET = client.NET
		inv.NetDays = client.NetDays
	}

	if dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "[update] %s\n", invoiceNumber)
		printInvoiceSummary(cmd, clientName, invoiceNumber, amount, hours, rate, role, billedFrom, billedTo, inv.NET, inv.NetDays, repoNames, agg.NokoDescs, desc)
		return nil
	}

	if err := nc.UpdateInvoice(ctx, existing.PageID, inv); err != nil {
		return fmt.Errorf("update invoice %s: %w", invoiceNumber, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "updated %s (%s): amount=$%.2f\n", invoiceNumber, clientName, amount)
	return nil
}

func sortedRepos(agg *projectAgg) []string {
	names := make([]string, 0, len(agg.Repos))
	for r := range agg.Repos {
		names = append(names, r)
	}
	sort.Strings(names)
	return names
}

func printInvoiceSummary(cmd *cobra.Command, clientName, number string, amount, hours, rate float64, role, from, to, net string, netDays int, repos []string, nokoDescs []string, desc string) {
	fmt.Fprintf(cmd.OutOrStdout(), "─── %s ───\n", clientName)
	fmt.Fprintf(cmd.OutOrStdout(), "  number:  %s\n", number)
	fmt.Fprintf(cmd.OutOrStdout(), "  amount:  $%.2f (%.2fh × $%.0f %s)\n", amount, hours, rate, role)
	fmt.Fprintf(cmd.OutOrStdout(), "  dates:   %s → %s\n", from, to)
	fmt.Fprintf(cmd.OutOrStdout(), "  net:     %s (%d days)\n", net, netDays)
	fmt.Fprintf(cmd.OutOrStdout(), "  repos:   %s\n", strings.Join(repos, ", "))
	if len(nokoDescs) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  logs:\n")
		for _, d := range nokoDescs {
			fmt.Fprintf(cmd.OutOrStdout(), "    • %s\n", d)
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  desc:    %s\n", desc)
}

// buildInvoiceDescription builds the Notion invoice description.
// If an llmClient is provided, it uses LLM to summarize Noko logs + commit messages.
// Otherwise falls back to a concatenation of the top commit messages.
func buildInvoiceDescription(month string, repos []string, nokoDescs []string, commitMsgs []string, llmClient *llm.Client) string {
	if llmClient != nil {
		// Prefer Noko log descriptions (already summarized per session) then supplement with raw commits.
		var items []string
		items = append(items, nokoDescs...)
		items = append(items, commitMsgs...)
		if len(items) > 0 {
			summary, err := llmClient.SummarizeForInvoice(items)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warn: LLM invoice summary: %v\n", err)
			} else if summary != "" {
				if len(summary) > 150 {
					summary = summary[:147] + "..."
				}
				return summary
			}
		}
	}

	// Fallback: classic format.
	parts := []string{month + " development"}
	if len(repos) > 0 {
		parts = append(parts, "("+strings.Join(repos, ", ")+")")
	}
	desc := strings.Join(parts, " ")
	if len(commitMsgs) > 0 {
		top := commitMsgs
		if len(top) > 3 {
			top = top[:3]
		}
		desc += " — " + strings.Join(top, "; ")
	}
	if len(desc) > 200 {
		desc = desc[:197] + "..."
	}
	return desc
}
