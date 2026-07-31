package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/bean-la/wrklogr/internal/config"
	"github.com/bean-la/wrklogr/internal/invoicesnapshot"
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

// invoiceLine holds one invoice row after resolving clients/rates/description (shared by CLI and wizard).
type invoiceLine struct {
	ClientName    string
	InvoiceNumber string
	Amount        float64
	Hours         float64
	Rate          float64
	Role          string
	BilledFrom    string
	BilledTo      string
	NET           string
	NetDays       int
	Repos         []string
	NokoDescs     []string
	Desc          string
	ClientPageID  string
	TargetPageID  string // existing Notion page for updates
}

type notionInvoiceOpts struct {
	SinceInput    string
	UntilInput    string
	InvoiceNumber string
	Update        bool
	Author        string
	RepoPatterns  []string
	LocalPaths    []string
	GithubToken   string
	NotionToken   string
	DryRun        bool
	SilentFetch   bool
	DescOverride  string
	AttachPDF        bool
	NoSnapshot       bool
	DescriptionOnly  bool
}

type notionInvoiceFetchOpts struct {
	SinceInput    string
	UntilInput    string
	Author        string
	RepoPatterns  []string
	LocalPaths    []string
	GithubToken   string
	SilentFetch   bool
}

type notionInvoiceFetched struct {
	Since      time.Time
	Until      time.Time
	BilledFrom string
	BilledTo   string
	MonthLabel string
	Aggs       map[int]*projectAgg
	LLMClient  *llm.Client
}

func resolveNotionAPIToken(cfg *config.RuntimeConfig, notionFlag string) string {
	token := strings.TrimSpace(notionFlag)
	if token == "" {
		token = strings.TrimSpace(os.Getenv("NOTION_TOKEN"))
	}
	if token == "" && cfg.Notion != nil {
		token = strings.TrimSpace(cfg.Notion.APIToken)
	}
	return token
}

func notionInvoiceFlagChanged(cmd *cobra.Command) bool {
	names := []string{"update", "invoice-number", "since", "until", "author", "repo", "dry-run", "local-path", "notion-token", "token", "attach-pdf"}
	for _, name := range names {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func notionPublicPageURL(pageID string) string {
	s := strings.ReplaceAll(strings.TrimSpace(pageID), "-", "")
	if s == "" {
		return ""
	}
	return "https://www.notion.so/" + s
}

func fetchInvoicePipeline(ctx context.Context, cmd *cobra.Command, cfg *config.RuntimeConfig, opts notionInvoiceFetchOpts) (*notionInvoiceFetched, error) {
	since, until, err := resolveBillingRange(opts.SinceInput, opts.UntilInput)
	if err != nil {
		return nil, err
	}

	var days []session.DaySummary

	if len(opts.LocalPaths) > 0 {
		var merged []session.Commit
		for _, p := range opts.LocalPaths {
			commits, scanErr := localgit.ListCommits(p, &since, &until)
			if scanErr != nil {
				return nil, fmt.Errorf("git log %s: %w", p, scanErr)
			}
			label := filepath.Base(p)
			for _, c := range commits {
				if opts.Author != "" && !strings.EqualFold(c.AuthorName, opts.Author) {
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
		ghTok := strings.TrimSpace(opts.GithubToken)
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
		reportOut := io.Writer(cmd.OutOrStdout())
		if opts.SilentFetch {
			reportOut = io.Discard
		}
		result, reportErr := runReport(ctx, reportOut, reportOpts{
			Repos:       cfg.Repos,
			Since:       &since,
			Until:       &until,
			Author:      opts.Author,
			SessionGap:  cfg.SessionGap,
			Timezone:    cfg.Timezone,
			NokoConfig:  cfg.Noko,
			GitHubToken: ghTok,
			LLMConfig:   &llmCfg,
		})
		if reportErr != nil {
			return nil, fmt.Errorf("fetch commits: %w", reportErr)
		}
		days = result.Days
	}

	if len(opts.RepoPatterns) > 0 {
		days = filterDaysByRepo(days, opts.RepoPatterns)
	}

	aggs := aggregateByProject(days, cfg.Noko)
	if len(aggs) == 0 {
		return &notionInvoiceFetched{
			Since: since, Until: until,
			BilledFrom: since.Format("2006-01-02"),
			BilledTo:   until.Format("2006-01-02"),
			MonthLabel: since.Format("January 2006"),
			Aggs:       aggs,
		}, nil
	}

	billedFrom := since.Format("2006-01-02")
	billedTo := until.Format("2006-01-02")
	monthLabel := since.Format("January 2006")

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

	llmCfg := resolveLLMConfig(cfg, "", "")
	var llmClient *llm.Client
	if llmCfg.APIKey != "" {
		llmClient = llm.NewClient(llmCfg)
	}

	return &notionInvoiceFetched{
		Since: since, Until: until,
		BilledFrom: billedFrom,
		BilledTo:   billedTo,
		MonthLabel: monthLabel,
		Aggs:       aggs,
		LLMClient:  llmClient,
	}, nil
}

func collectCreateInvoiceLines(
	ctx context.Context,
	nc *notion.Client,
	cfg *config.RuntimeConfig,
	invoiceNumber, role string,
	aggs map[int]*projectAgg,
	billedFrom, billedTo, monthLabel string,
	llmClient *llm.Client,
	descOverride string,
) []invoiceLine {
	projIDs := make([]int, 0, len(aggs))
	for id := range aggs {
		projIDs = append(projIDs, id)
	}
	sort.Ints(projIDs)

	out := make([]invoiceLine, 0, len(projIDs))
	for _, projID := range projIDs {
		agg := aggs[projID]
		hours := float64(agg.Minutes) / 60.0

		client, err := nc.FindClientByNokoProjectID(ctx, cfg.Notion.ClientsDBID, projID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: notion lookup for project %d: %v\n", projID, err)
			continue
		}
		if client == nil {
			fmt.Fprintf(os.Stderr, "warn: no Notion client for Noko project %d — skipping\n", projID)
			continue
		}

		rate := client.RateForRole(role)
		if rate == 0 {
			rate = cfg.Notion.DefaultRate
		}
		days := hours / 8.0
		amount := days * rate
		repoNames := sortedRepos(agg)

		desc := strings.TrimSpace(descOverride)
		if desc == "" {
			desc = buildInvoiceDescription(monthLabel, repoNames, agg.NokoDescs, agg.Msgs, llmClient)
		}

		out = append(out, invoiceLine{
			ClientName:    client.Name,
			InvoiceNumber: invoiceNumber,
			Amount:        amount,
			Hours:         hours,
			Rate:          rate,
			Role:          role,
			BilledFrom:    billedFrom,
			BilledTo:      billedTo,
			NET:           client.NET,
			NetDays:       client.NetDays,
			Repos:         repoNames,
			NokoDescs:     agg.NokoDescs,
			Desc:          desc,
			ClientPageID:  client.PageID,
		})
	}
	return out
}

func mergeProjectAggs(aggs map[int]*projectAgg) *projectAgg {
	merged := &projectAgg{
		Repos:   make(map[string]struct{}),
		msgSeen: make(map[string]struct{}),
	}
	for _, a := range aggs {
		merged.Minutes += a.Minutes
		for r := range a.Repos {
			merged.Repos[r] = struct{}{}
		}
		for _, m := range a.Msgs {
			if _, seen := merged.msgSeen[m]; !seen {
				merged.msgSeen[m] = struct{}{}
				merged.Msgs = append(merged.Msgs, m)
			}
		}
		merged.NokoDescs = append(merged.NokoDescs, a.NokoDescs...)
	}
	return merged
}

// resolveUpdateAgg picks session/Noko data for an update. When the invoice Client is set,
// only that client's Noko project is used — never all projects.
func resolveUpdateAgg(client *notion.ClientRecord, aggs map[int]*projectAgg) *projectAgg {
	if client != nil && client.NokoProjectID != 0 {
		if agg, ok := aggs[client.NokoProjectID]; ok {
			return agg
		}
		return &projectAgg{
			Repos:   make(map[string]struct{}),
			msgSeen: make(map[string]struct{}),
		}
	}
	switch len(aggs) {
	case 0:
		return &projectAgg{
			Repos:   make(map[string]struct{}),
			msgSeen: make(map[string]struct{}),
		}
	case 1:
		for _, a := range aggs {
			return a
		}
	}
	fmt.Fprintf(os.Stderr, "warn: invoice has no Client on page — combining %d projects; link Client in Notion or pass --repo\n", len(aggs))
	return mergeProjectAggs(aggs)
}

func fetchNokoDescsForProject(ctx context.Context, projectID int, billedFrom, billedTo string) []string {
	if projectID == 0 {
		return nil
	}
	nokoToken := strings.TrimSpace(os.Getenv("NOKO_TOKEN"))
	if nokoToken == "" {
		return nil
	}
	nokoClient := noko.NewClient(nokoToken, nil)
	entries, err := nokoClient.ListEntries(ctx, billedFrom, billedTo, nil, []int{projectID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: fetch noko entries for project %d: %v\n", projectID, err)
		return nil
	}
	var descs []string
	for _, e := range entries {
		if d := strings.TrimSpace(e.Description); d != "" {
			descs = append(descs, d)
		}
	}
	return descs
}

func collectUpdateInvoiceLine(
	ctx context.Context,
	nc *notion.Client,
	cfg *config.RuntimeConfig,
	invoiceNumber, role string,
	aggs map[int]*projectAgg,
	billedFrom, billedTo, monthLabel string,
	llmClient *llm.Client,
	descOverride string,
) (*invoiceLine, error) {
	if invoiceNumber == "" {
		return nil, fmt.Errorf("--update requires --invoice-number")
	}

	existing, err := nc.FindInvoiceByNumber(ctx, cfg.Notion.InvoiceDBID, invoiceNumber)
	if err != nil {
		return nil, fmt.Errorf("find invoice %s: %w", invoiceNumber, err)
	}
	if existing == nil {
		return nil, fmt.Errorf("invoice %s not found in Notion", invoiceNumber)
	}

	var client *notion.ClientRecord
	if existing.ClientPageID != "" {
		client, err = nc.GetClientPage(ctx, existing.ClientPageID)
		if err != nil {
			return nil, fmt.Errorf("get client page for %s: %w", invoiceNumber, err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "warn: invoice %s has no Client relation in Notion — link the client page to scope description and PDF entries\n", invoiceNumber)
	}

	agg := resolveUpdateAgg(client, aggs)
	if client != nil && client.NokoProjectID != 0 && len(agg.NokoDescs) == 0 {
		agg.NokoDescs = fetchNokoDescsForProject(ctx, client.NokoProjectID, billedFrom, billedTo)
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
	days := hours / 8.0
	amount := days * rate
	repoNames := sortedRepos(agg)

	desc := strings.TrimSpace(descOverride)
	if desc == "" {
		desc = buildInvoiceDescription(monthLabel, repoNames, agg.NokoDescs, agg.Msgs, llmClient)
	}

	net := ""
	netDays := 0
	clientPageID := ""
	if client != nil {
		net = client.NET
		netDays = client.NetDays
		clientPageID = client.PageID
	}

	return &invoiceLine{
		ClientName:    clientName,
		InvoiceNumber: invoiceNumber,
		Amount:        amount,
		Hours:         hours,
		Rate:          rate,
		Role:          role,
		BilledFrom:    billedFrom,
		BilledTo:      billedTo,
		NET:           net,
		NetDays:       netDays,
		Repos:         repoNames,
		NokoDescs:     agg.NokoDescs,
		Desc:          desc,
		ClientPageID:  clientPageID,
		TargetPageID:  existing.PageID,
	}, nil
}

func writeCreateInvoices(cmd *cobra.Command, ctx context.Context, nc *notion.Client, cfg *config.RuntimeConfig, dryRun, attachPDF bool, snap invoiceSnapshotOpts, lines []invoiceLine) error {
	for _, line := range lines {
		if dryRun {
			printInvoiceSummary(cmd, line.ClientName, line.InvoiceNumber, line.Amount, line.Hours, line.Rate, line.Role, line.BilledFrom, line.BilledTo, line.NET, line.NetDays, line.Repos, line.NokoDescs, line.Desc)
			if attachPDF {
				fmt.Fprintf(cmd.OutOrStdout(), "  [dry-run] would generate PDF locally and attach to Notion\n")
			}
			continue
		}
		saveInvoiceSnapshot(cmd.OutOrStdout(), cfg, nc, ctx, snap, "create", &line, nil)
		inv := notion.InvoiceRequest{
			InvoiceNumber: line.InvoiceNumber,
			Amount:        line.Amount,
			BilledFrom:    line.BilledFrom,
			BilledTo:      line.BilledTo,
			ClientPageID:  line.ClientPageID,
			Description:   line.Desc,
			NET:           line.NET,
			NetDays:       line.NetDays,
		}
		pageID, err := nc.CreateInvoice(ctx, cfg.Notion.InvoiceDBID, inv)
		if err != nil {
			return fmt.Errorf("create invoice for %s: %w", line.ClientName, err)
		}
		url := notionPublicPageURL(pageID)
		fmt.Fprintf(cmd.OutOrStdout(), "created invoice for %s: %s amount=$%.2f\n", line.ClientName, url, line.Amount)
		if attachPDF {
			line.TargetPageID = pageID
			if err := attachInvoicePDF(cmd, cfg, nc, pageID, line.InvoiceNumber, false, snap); err != nil {
				return fmt.Errorf("attach pdf for %s: %w", line.ClientName, err)
			}
		}
	}
	return nil
}

func writeUpdateInvoice(cmd *cobra.Command, ctx context.Context, nc *notion.Client, cfg *config.RuntimeConfig, invoiceNumber string, dryRun, attachPDF, descriptionOnly bool, snap invoiceSnapshotOpts, line *invoiceLine) error {
	if dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "[update] %s\n", invoiceNumber)
		printInvoiceSummary(cmd, line.ClientName, line.InvoiceNumber, line.Amount, line.Hours, line.Rate, line.Role, line.BilledFrom, line.BilledTo, line.NET, line.NetDays, line.Repos, line.NokoDescs, line.Desc)
		if attachPDF {
			return attachInvoicePDF(cmd, cfg, nc, line.TargetPageID, invoiceNumber, true, snap)
		}
		return nil
	}

	saveInvoiceSnapshot(cmd.OutOrStdout(), cfg, nc, ctx, snap, "update", line, nil)

	if descriptionOnly {
		if err := nc.UpdateInvoiceDescription(ctx, line.TargetPageID, line.Desc); err != nil {
			return fmt.Errorf("update description for %s: %w", invoiceNumber, err)
		}
		url := notionPublicPageURL(line.TargetPageID)
		fmt.Fprintf(cmd.OutOrStdout(), "updated description for %s (%s): %s\n", invoiceNumber, line.ClientName, url)
	} else {
		inv := notion.InvoiceRequest{
			Amount:      line.Amount,
			BilledFrom:  line.BilledFrom,
			BilledTo:    line.BilledTo,
			Description: line.Desc,
			NET:         line.NET,
			NetDays:     line.NetDays,
		}
		if err := nc.UpdateInvoice(ctx, line.TargetPageID, inv); err != nil {
			return fmt.Errorf("update invoice %s: %w", invoiceNumber, err)
		}
		url := notionPublicPageURL(line.TargetPageID)
		fmt.Fprintf(cmd.OutOrStdout(), "updated %s (%s): %s amount=$%.2f\n", invoiceNumber, line.ClientName, url, line.Amount)
	}
	if attachPDF {
		if err := attachInvoicePDF(cmd, cfg, nc, line.TargetPageID, invoiceNumber, false, snap); err != nil {
			return fmt.Errorf("attach pdf: %w", err)
		}
	}
	return nil
}

func executeNotionInvoice(cmd *cobra.Command, cfg *config.RuntimeConfig, opts notionInvoiceOpts) error {
	if cfg.Notion == nil {
		return fmt.Errorf("notion config required: add [notion] section to wrklogr.toml")
	}
	if cfg.Noko == nil {
		return fmt.Errorf("noko config required for project mapping: add [noko] section to wrklogr.toml")
	}

	token := resolveNotionAPIToken(cfg, opts.NotionToken)
	if token == "" {
		return fmt.Errorf("notion API token required: set --notion-token, NOTION_TOKEN, or notion.api_token in config")
	}

	ctx := context.Background()
	fetched, err := fetchInvoicePipeline(ctx, cmd, cfg, notionInvoiceFetchOpts{
		SinceInput:    opts.SinceInput,
		UntilInput:    opts.UntilInput,
		Author:        opts.Author,
		RepoPatterns:  opts.RepoPatterns,
		LocalPaths:    opts.LocalPaths,
		GithubToken:   opts.GithubToken,
		SilentFetch:   opts.SilentFetch,
	})
	if err != nil {
		return err
	}
	if len(fetched.Aggs) == 0 && !(opts.Update && opts.DescriptionOnly) {
		fmt.Fprintln(cmd.OutOrStdout(), "no sessions found for the given date range")
		return nil
	}

	nc := notion.NewClient(token, nil)

	role := cfg.Notion.Role
	if role == "" {
		role = "backend"
	}

	snap := invoiceSnapshotOpts{
		Enabled: !opts.NoSnapshot && !opts.DryRun,
		Fetched: fetched,
		FetchMeta: invoicesnapshot.FetchMeta{
			Since:        opts.SinceInput,
			Until:        opts.UntilInput,
			BilledFrom:   fetched.BilledFrom,
			BilledTo:     fetched.BilledTo,
			MonthLabel:   fetched.MonthLabel,
			Author:       opts.Author,
			RepoPatterns: opts.RepoPatterns,
			LocalPaths:   opts.LocalPaths,
		},
	}

	if opts.Update {
		line, collErr := collectUpdateInvoiceLine(ctx, nc, cfg, opts.InvoiceNumber, role, fetched.Aggs, fetched.BilledFrom, fetched.BilledTo, fetched.MonthLabel, fetched.LLMClient, opts.DescOverride)
		if collErr != nil {
			return collErr
		}
		return writeUpdateInvoice(cmd, ctx, nc, cfg, opts.InvoiceNumber, opts.DryRun, opts.AttachPDF, opts.DescriptionOnly, snap, line)
	}

	lines := collectCreateInvoiceLines(ctx, nc, cfg, opts.InvoiceNumber, role, fetched.Aggs, fetched.BilledFrom, fetched.BilledTo, fetched.MonthLabel, fetched.LLMClient, opts.DescOverride)
	return writeCreateInvoices(cmd, ctx, nc, cfg, opts.DryRun, opts.AttachPDF, snap, lines)
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
	var attachPDF bool
	var noSnapshot bool
	var descriptionOnly bool
	var descOverride string

	cmd := &cobra.Command{
		Use:   "notion-invoice",
		Short: "Create a draft invoice page in Notion for the given date range",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := getConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if len(args) > 0 {
				return fmt.Errorf("unexpected arguments %v — did you mean \"wrklogr notion-invoice wizard\"?", args)
			}
			if !notionInvoiceFlagChanged(cmd) {
				return runNotionInvoiceWizard(cmd, getConfig)
			}
			return executeNotionInvoice(cmd, cfg, notionInvoiceOpts{
				SinceInput:    sinceInput,
				UntilInput:    untilInput,
				InvoiceNumber: invoiceNumber,
				Update:        update,
				Author:        author,
				RepoPatterns:  repoPatterns,
				LocalPaths:    localPaths,
				GithubToken:   githubToken,
				NotionToken:   notionToken,
				DryRun:        dryRun,
				SilentFetch:   false,
				AttachPDF:     attachPDF,
				NoSnapshot:       noSnapshot,
				DescriptionOnly:  descriptionOnly,
				DescOverride:     descOverride,
			})
		},
	}

	wizardCmd := &cobra.Command{
		Use:   "wizard",
		Short: "Interactive invoicing wizard",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("unexpected arguments %v", args)
			}
			return runNotionInvoiceWizard(cmd, getConfig)
		},
	}
	cmd.AddCommand(wizardCmd)
	cmd.AddCommand(newNotionInvoiceAttachPDFCmd(getConfig))
	cmd.AddCommand(newNotionInvoicePrintPDFCmd(getConfig))

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
	cmd.Flags().BoolVar(&attachPDF, "attach-pdf", false, "Attach invoice PDF from bean-invoicing after create/update")
	cmd.PersistentFlags().BoolVar(&noSnapshot, "no-snapshot", false, "Skip saving invoice backup snapshots to disk")
	cmd.Flags().BoolVar(&descriptionOnly, "description-only", false, "Update only Description (keep Amount and dates unchanged)")
	cmd.Flags().StringVar(&descOverride, "description", "", "Use this description text instead of generating one")

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
				rebuilt, ok := session.SessionFromCommits(commits)
				if ok {
					sessions = append(sessions, rebuilt)
				}
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
