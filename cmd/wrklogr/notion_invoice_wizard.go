package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"

	"github.com/bean-la/wrklogr/internal/config"
	"github.com/bean-la/wrklogr/internal/notion"
	"github.com/spf13/cobra"
)

const (
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiReset  = "\033[0m"
	ansiYellow = "\033[33m"
	ansiGreen  = "\033[32m"
)

func runNotionInvoiceWizard(cmd *cobra.Command, getConfig func() (*config.RuntimeConfig, error)) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		fmt.Fprintln(cmd.OutOrStdout(), "\nAborted.")
		os.Exit(130)
	}()

	cfg, err := getConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	printWizardEnvWarnings(cmd, cfg)

	if cfg.Notion == nil {
		return fmt.Errorf("notion config required: add [notion] section to wrklogr.toml")
	}
	if cfg.Noko == nil {
		return fmt.Errorf("noko config required for project mapping: add [noko] section to wrklogr.toml")
	}
	if len(cfg.Repos) == 0 {
		return fmt.Errorf("no repositories configured in wrklogr.toml — wizard uses GitHub API mode")
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 4096), 1024*1024)

	fmt.Fprintf(cmd.OutOrStdout(), "\n%s─── Notion invoice wizard ───%s\n\n", ansiBold+ansiGreen, ansiReset)

	action := strings.ToLower(strings.TrimSpace(promptRequired(cmd, sc, "Create new invoice or update existing? [create/update]", "")))
	for action != "create" && action != "update" {
		fmt.Fprintf(cmd.OutOrStdout(), "%sEnter \"create\" or \"update\".%s\n", ansiYellow, ansiReset)
		action = strings.ToLower(strings.TrimSpace(promptRequired(cmd, sc, "Create new invoice or update existing? [create/update]", "")))
	}
	updateMode := action == "update"

	token := resolveNotionAPIToken(cfg, "")
	ctx := context.Background()
	var nc *notion.Client
	if strings.TrimSpace(token) != "" {
		nc = notion.NewClient(token, nil)
	}

	var invoiceNumber string
	if updateMode {
		if nc == nil {
			return fmt.Errorf("notion API token required to look up an invoice — set NOTION_TOKEN or notion.api_token")
		}
		for {
			invoiceNumber = strings.TrimSpace(promptRequired(cmd, sc, "Invoice number (e.g. ADV-805)", ""))
			if invoiceNumber == "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%sInvoice number is required for updates.%s\n", ansiYellow, ansiReset)
				continue
			}
			rec, ferr := nc.FindInvoiceByNumber(ctx, cfg.Notion.InvoiceDBID, invoiceNumber)
			if ferr != nil {
				return fmt.Errorf("find invoice %s: %w", invoiceNumber, ferr)
			}
			if rec == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%sNo invoice %s in Notion. Try again.%s\n", ansiYellow, invoiceNumber, ansiReset)
				continue
			}
			clientName := "(unknown client)"
			if rec.ClientPageID != "" {
				cl, cerr := nc.GetClientPage(ctx, rec.ClientPageID)
				if cerr != nil {
					fmt.Fprintf(os.Stderr, "warn: load client page: %v\n", cerr)
				} else if cl != nil {
					clientName = cl.Name
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%sFound%s invoice %s — client %s — recorded amount %s\n\n",
				ansiBold, ansiReset, invoiceNumber, clientName, formatUSDComma(rec.Amount))
			break
		}
	} else {
		invoiceNumber = strings.TrimSpace(promptRequired(cmd, sc, "Invoice number (leave blank to auto-assign)", ""))
	}

	defSince, defUntil, err := resolveBillingRange("", "")
	if err != nil {
		return err
	}
	sinceInput := ""
	untilInput := ""
	fmt.Fprintf(cmd.OutOrStdout(), "%sDefault billing period:%s %s → %s\n",
		ansiDim, ansiReset, defSince.Format("2006-01-02"), defUntil.Format("2006-01-02"))
	if promptYesNo(cmd, sc, "Use this billing period?", true) {
		sinceInput, untilInput = "", ""
	} else {
		for {
			sinceInput = strings.TrimSpace(promptRequired(cmd, sc, "Billing start (YYYY-MM-DD)", ""))
			if perr := parseWizardDate(sinceInput); perr != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s%s%s\n", ansiYellow, perr.Error(), ansiReset)
				continue
			}
			break
		}
		for {
			untilInput = strings.TrimSpace(promptRequired(cmd, sc, "Billing end (YYYY-MM-DD)", ""))
			if perr := parseWizardDate(untilInput); perr != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s%s%s\n", ansiYellow, perr.Error(), ansiReset)
				continue
			}
			break
		}
	}

	defaultLogin := inferGitHubLogin()
	authorRaw := strings.TrimSpace(promptRequired(cmd, sc, "GitHub login for commit filter", defaultLogin))
	author := authorRaw
	if strings.TrimSpace(author) == "" {
		return fmt.Errorf("could not determine GitHub login — install gh CLI and authenticate, or enter a login explicitly")
	}

	repoPatterns, err := wizardRepoSelection(cmd, sc, cfg.Repos)
	if err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "\n%sFetching commits and invoice data…%s\n", ansiDim, ansiReset)

	fetched, err := fetchInvoicePipeline(ctx, cmd, cfg, notionInvoiceFetchOpts{
		SinceInput:    sinceInput,
		UntilInput:    untilInput,
		Author:        author,
		RepoPatterns:  repoPatterns,
		LocalPaths:    nil,
		GithubToken:   "",
		SilentFetch:   true,
	})
	if err != nil {
		return err
	}
	if len(fetched.Aggs) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no sessions found for the given date range")
		return nil
	}

	token = resolveNotionAPIToken(cfg, "")
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("notion API token required — set NOTION_TOKEN or notion.api_token before preview/write")
	}
	nc = notion.NewClient(token, nil)

	role := cfg.Notion.Role
	if role == "" {
		role = "backend"
	}

	var lines []invoiceLine
	var updateLine *invoiceLine

	if updateMode {
		ul, cerr := collectUpdateInvoiceLine(ctx, nc, cfg, invoiceNumber, role, fetched.Aggs, fetched.BilledFrom, fetched.BilledTo, fetched.MonthLabel, fetched.LLMClient, "")
		if cerr != nil {
			return cerr
		}
		updateLine = ul
	} else {
		lines = collectCreateInvoiceLines(ctx, nc, cfg, invoiceNumber, role, fetched.Aggs, fetched.BilledFrom, fetched.BilledTo, fetched.MonthLabel, fetched.LLMClient, "")
		if len(lines) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No invoice targets after resolving Notion clients.")
			return nil
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "\n%s── Preview ──%s\n", ansiBold, ansiReset)
	if updateMode {
		printWizardPreview(cmd.OutOrStdout(), *updateLine)
	} else {
		for i := range lines {
			if len(lines) > 1 {
				fmt.Fprintf(cmd.OutOrStdout(), "\n%sClient %d · %s%s\n", ansiBold, i+1, lines[i].ClientName, ansiReset)
			}
			printWizardPreview(cmd.OutOrStdout(), lines[i])
		}
	}

	canEditDesc := updateMode || len(lines) == 1
	if canEditDesc && promptYesNo(cmd, sc, "Update description?", false) {
		useEditor := promptYesNo(cmd, sc, "Open $EDITOR?", true)
		var current string
		if updateMode {
			current = updateLine.Desc
		} else {
			current = lines[0].Desc
		}
		var edited string
		var eerr error
		if useEditor {
			edited, eerr = editDescriptionInEditor(cmd, current)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "%sNew description (single line):%s ", ansiDim, ansiReset)
			if !sc.Scan() {
				return fmt.Errorf("read stdin: %w", sc.Err())
			}
			edited = strings.TrimSpace(sc.Text())
		}
		if eerr != nil {
			return eerr
		}
		if edited != "" {
			if updateMode {
				updateLine.Desc = edited
			} else {
				lines[0].Desc = edited
			}
		}
	} else if !canEditDesc {
		fmt.Fprintf(cmd.OutOrStdout(), "%sSkipping description edit — multiple invoice targets.%s\n", ansiDim, ansiReset)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "\n%sReady:%s\n", ansiBold, ansiReset)
	if updateMode {
		fmt.Fprintln(cmd.OutOrStdout(), wizardFinalSummaryLine(*updateLine))
	} else {
		for _, ln := range lines {
			fmt.Fprintln(cmd.OutOrStdout(), wizardFinalSummaryLine(ln))
		}
	}

	if notionInvoiceDryRunInherited(cmd) {
		fmt.Fprintf(cmd.OutOrStdout(), "\n%s--dry-run set — preview only (no Notion write).%s\n", ansiDim, ansiReset)
		return nil
	}

	if !promptYesNo(cmd, sc, "Write to Notion?", false) {
		fmt.Fprintln(cmd.OutOrStdout(), "Skipped write.")
		return nil
	}

	if updateMode {
		return writeUpdateInvoice(cmd, ctx, nc, cfg, invoiceNumber, false, updateLine)
	}
	return writeCreateInvoices(cmd, ctx, nc, cfg, false, lines)
}

func wizardFinalSummaryLine(line invoiceLine) string {
	num := strings.TrimSpace(line.InvoiceNumber)
	if num == "" {
		num = "(auto)"
	}
	short := line.Desc
	if len(short) > 90 {
		short = short[:87] + "..."
	}
	return fmt.Sprintf("%s · %s · %.2fh · %s · %q", num, line.ClientName, line.Hours, formatUSDComma(line.Amount), short)
}

func printWizardPreview(out io.Writer, line invoiceLine) {
	fmt.Fprintf(out, "%sHours & amount%s  %.2fh   %s\n", ansiBold, ansiReset, line.Hours, formatUSDComma(line.Amount))
	if len(line.NokoDescs) > 0 {
		fmt.Fprintf(out, "%slogs:%s\n", ansiBold, ansiReset)
		for _, d := range line.NokoDescs {
			fmt.Fprintf(out, "  • %s\n", d)
		}
	} else {
		fmt.Fprintf(out, "%slogs:%s (none)\n", ansiDim, ansiReset)
	}
	fmt.Fprintf(out, "%sDescription (LLM / fallback)%s\n  %s\n", ansiBold, ansiReset, line.Desc)
}

func formatUSDComma(amount float64) string {
	cents := int64(math.Round(amount * 100))
	neg := cents < 0
	if neg {
		cents = -cents
	}
	dollars := cents / 100
	frac := cents % 100
	ds := insertThousandsSep(strconv.FormatInt(dollars, 10))
	s := fmt.Sprintf("$%s.%02d", ds, frac)
	if neg {
		return "-" + s
	}
	return s
}

func insertThousandsSep(intDigits string) string {
	if len(intDigits) <= 3 {
		return intDigits
	}
	var b strings.Builder
	lead := len(intDigits) % 3
	if lead == 0 {
		lead = 3
	}
	b.WriteString(intDigits[:lead])
	for i := lead; i < len(intDigits); i += 3 {
		b.WriteByte(',')
		b.WriteString(intDigits[i : i+3])
	}
	return b.String()
}

func editDescriptionInEditor(cmd *cobra.Command, initial string) (string, error) {
	ed := strings.TrimSpace(os.Getenv("EDITOR"))
	if ed == "" {
		ed = "vim"
	}
	f, err := os.CreateTemp("", "wrklogr-invoice-desc-*.txt")
	if err != nil {
		return "", fmt.Errorf("temp file: %w", err)
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()

	if _, err := f.WriteString(initial); err != nil {
		f.Close()
		return "", fmt.Errorf("write temp: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close temp: %w", err)
	}

	c := exec.Command(ed, path)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("editor %s: %w", ed, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read edited file: %w", err)
	}
	return strings.TrimSpace(string(body)), nil
}

func parseWizardDate(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("date is required")
	}
	if _, err := parseDateBound(s, false); err != nil {
		return fmt.Errorf("invalid date %q — use YYYY-MM-DD", s)
	}
	return nil
}

func inferGitHubLogin() string {
	out, err := exec.Command("gh", "api", "user", "--jq", ".login").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func wizardRepoSelection(cmd *cobra.Command, sc *bufio.Scanner, repos []string) ([]string, error) {
	selected := make([]bool, len(repos))
	for i := range selected {
		selected[i] = true
	}
	useGlob := false
	var globPatterns []string

	for {
		fmt.Fprintf(cmd.OutOrStdout(), "\n%sRepos%s — toggle numbers to include/exclude; type %sg%s then Enter for glob patterns; blank Enter confirms.\n",
			ansiBold, ansiReset, ansiBold, ansiReset)
		for i, r := range repos {
			check := "[ ]"
			if selected[i] {
				check = "[x]"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  %s %3d. %s\n", check, i+1, r)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s> %s", ansiDim, ansiReset)
		if !sc.Scan() {
			return nil, fmt.Errorf("read stdin: %w", sc.Err())
		}
		line := strings.TrimSpace(sc.Text())
		if strings.EqualFold(line, "g") || strings.EqualFold(line, "glob") {
			raw := strings.TrimSpace(promptRequired(cmd, sc, "Glob patterns (comma-separated)", ""))
			if raw == "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%sNo patterns entered.%s\n", ansiYellow, ansiReset)
				continue
			}
			parts := strings.Split(raw, ",")
			globPatterns = globPatterns[:0]
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					globPatterns = append(globPatterns, p)
				}
			}
			if len(globPatterns) == 0 {
				continue
			}
			useGlob = true
			break
		}
		if line == "" {
			if !useGlob && countSelectedRepos(selected) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "%sSelect at least one repository.%s\n", ansiYellow, ansiReset)
				continue
			}
			break
		}
		fields := strings.Fields(line)
		for _, f := range fields {
			n, err := strconv.Atoi(f)
			if err != nil || n < 1 || n > len(repos) {
				fmt.Fprintf(cmd.OutOrStdout(), "%sInvalid index %q — use repo numbers.%s\n", ansiYellow, f, ansiReset)
				continue
			}
			selected[n-1] = !selected[n-1]
		}
	}

	if useGlob {
		fmt.Fprintf(cmd.OutOrStdout(), "%sUsing repo glob patterns:%s %v\n", ansiDim, ansiReset, globPatterns)
		return globPatterns, nil
	}

	on := 0
	for _, b := range selected {
		if b {
			on++
		}
	}
	if on == len(repos) {
		return nil, nil
	}
	var patterns []string
	for i, pick := range selected {
		if pick {
			patterns = append(patterns, repos[i])
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%sIncluding %d / %d repos.%s\n", ansiDim, on, len(repos), ansiReset)
	return patterns, nil
}

func notionInvoiceDryRunInherited(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() != "notion-invoice" {
			continue
		}
		f := c.Flags().Lookup("dry-run")
		return f != nil && f.Changed
	}
	return false
}

func countSelectedRepos(selected []bool) int {
	n := 0
	for _, b := range selected {
		if b {
			n++
		}
	}
	return n
}

func promptRequired(cmd *cobra.Command, sc *bufio.Scanner, label, defShow string) string {
	fmt.Fprintf(cmd.OutOrStdout(), "%s%s%s: ", ansiBold, label, ansiReset)
	if defShow != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "%s[%s]%s ", ansiDim, defShow, ansiReset)
	}
	if !sc.Scan() {
		return defShow
	}
	v := strings.TrimSpace(sc.Text())
	if v == "" {
		return defShow
	}
	return v
}

func promptYesNo(cmd *cobra.Command, sc *bufio.Scanner, question string, defaultYes bool) bool {
	def := "y/N"
	if defaultYes {
		def = "Y/n"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s%s [%s]? %s", ansiBold, question, def, ansiReset)
	if !sc.Scan() {
		return defaultYes
	}
	s := strings.TrimSpace(strings.ToLower(sc.Text()))
	if s == "" {
		return defaultYes
	}
	return strings.HasPrefix(s, "y")
}

func printWizardEnvWarnings(cmd *cobra.Command, cfg *config.RuntimeConfig) {
	out := cmd.OutOrStdout()
	var msgs []string
	if resolveNotionAPIToken(cfg, "") == "" {
		msgs = append(msgs, "NOTION_TOKEN / notion.api_token is not set — Notion lookups and writes will fail until it is.")
	}
	nokoTok := strings.TrimSpace(os.Getenv("NOKO_TOKEN"))
	if nokoTok == "" && cfg.Noko != nil {
		nokoTok = strings.TrimSpace(cfg.Noko.APIToken)
	}
	if nokoTok == "" {
		msgs = append(msgs, "NOKO_TOKEN / noko.api_token is missing — Noko log descriptions will not appear on the invoice.")
	}
	llmCfg := resolveLLMConfig(cfg, "", "")
	if llmCfg.APIKey == "" {
		msgs = append(msgs, "LLM_API_TOKEN / LLM_API_KEY / llm.api_key is missing — descriptions fall back to commit/Noko concatenation.")
	}
	if len(msgs) == 0 {
		return
	}
	fmt.Fprintf(out, "\n%sEnvironment / config notices:%s\n", ansiYellow, ansiReset)
	for _, m := range msgs {
		fmt.Fprintf(out, "  • %s\n", m)
	}
	fmt.Fprintln(out)
}
