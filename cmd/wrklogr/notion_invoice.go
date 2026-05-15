package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bean-la/wrklogr/internal/config"
	"github.com/bean-la/wrklogr/internal/localgit"
	"github.com/bean-la/wrklogr/internal/notion"
	"github.com/bean-la/wrklogr/internal/session"
	"github.com/spf13/cobra"
)

type projectAgg struct {
	Minutes int
	Repos   map[string]struct{}
	Msgs    []string
	msgSeen map[string]struct{}
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
		if len(a.Msgs) < 5 {
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
	var localPaths []string
	var dryRun bool
	var update bool

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

			paths := localPaths
			if len(paths) == 0 {
				paths = []string{"."}
			}

			var merged []session.Commit
			for _, p := range paths {
				commits, scanErr := localgit.ListCommits(p, &since, &until)
				if scanErr != nil {
					return fmt.Errorf("git log %s: %w", p, scanErr)
				}
				label := filepath.Base(p)
				if p == "." {
					abs, absErr := filepath.Abs(".")
					if absErr == nil {
						label = filepath.Base(abs)
					}
				}
				for _, c := range commits {
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
			sessions := session.Build(merged, cfg.SessionGap)
			days := session.BucketByDay(sessions, cfg.Timezone)

			aggs := aggregateByProject(days, cfg.Noko)
			if len(aggs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no sessions found for the given date range")
				return nil
			}

			billedFrom := since.Format("2006-01-02")
			billedTo := until.Format("2006-01-02")
			monthLabel := since.Format("January 2006")

			nc := notion.NewClient(token, nil)
			ctx := context.Background()

			role := cfg.Notion.Role
			if role == "" {
				role = "backend"
			}

			if update {
				return runUpdate(cmd, ctx, nc, cfg, invoiceNumber, role, aggs, billedFrom, billedTo, monthLabel, dryRun)
			}
			return runCreate(cmd, ctx, nc, cfg, invoiceNumber, role, aggs, billedFrom, billedTo, monthLabel, dryRun)
		},
	}

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
		amount := hours * rate
		repoNames := sortedRepos(agg)
		desc := buildInvoiceDescription(monthLabel, hours, repoNames, agg.Msgs)

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
			printInvoiceSummary(cmd, client.Name, invoiceNumber, amount, hours, rate, role, billedFrom, billedTo, client.NET, client.NetDays, repoNames, desc)
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
		}
	}

	hours := float64(agg.Minutes) / 60.0
	rate := 0.0
	clientName := invoiceNumber
	if client != nil {
		rate = client.RateForRole(role)
		clientName = client.Name
	}
	amount := hours * rate
	repoNames := sortedRepos(agg)
	desc := buildInvoiceDescription(monthLabel, hours, repoNames, agg.Msgs)

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
		printInvoiceSummary(cmd, clientName, invoiceNumber, amount, hours, rate, role, billedFrom, billedTo, inv.NET, inv.NetDays, repoNames, desc)
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

func printInvoiceSummary(cmd *cobra.Command, clientName, number string, amount, hours, rate float64, role, from, to, net string, netDays int, repos []string, desc string) {
	fmt.Fprintf(cmd.OutOrStdout(), "─── %s ───\n", clientName)
	fmt.Fprintf(cmd.OutOrStdout(), "  number:  %s\n", number)
	fmt.Fprintf(cmd.OutOrStdout(), "  amount:  $%.2f (%.2fh × $%.0f %s)\n", amount, hours, rate, role)
	fmt.Fprintf(cmd.OutOrStdout(), "  dates:   %s → %s\n", from, to)
	fmt.Fprintf(cmd.OutOrStdout(), "  net:     %s (%d days)\n", net, netDays)
	fmt.Fprintf(cmd.OutOrStdout(), "  repos:   %s\n", strings.Join(repos, ", "))
	fmt.Fprintf(cmd.OutOrStdout(), "  desc:    %s\n", desc)
}

func buildInvoiceDescription(month string, hours float64, repos []string, msgs []string) string {
	parts := []string{month + " development"}
	if len(repos) > 0 {
		parts = append(parts, "("+strings.Join(repos, ", ")+")")
	}
	desc := strings.Join(parts, " ")
	if len(msgs) > 0 {
		top := msgs
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
