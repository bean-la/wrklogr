package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bean-la/wrklogr/internal/config"
	"github.com/bean-la/wrklogr/internal/invoicepdf"
	"github.com/bean-la/wrklogr/internal/notion"
	"github.com/spf13/cobra"
)

const defaultInvoicingURL = "https://bean-invoicing.herokuapp.com"

func resolveInvoicingBaseURL(cfg *config.RuntimeConfig) string {
	if v := strings.TrimSpace(os.Getenv("BEAN_INVOICING_URL")); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	if cfg.Notion != nil && strings.TrimSpace(cfg.Notion.InvoicingURL) != "" {
		return strings.TrimSuffix(strings.TrimSpace(cfg.Notion.InvoicingURL), "/")
	}
	return defaultInvoicingURL
}

func resolveInvoicingKey(cfg *config.RuntimeConfig) string {
	if v := strings.TrimSpace(os.Getenv("BEAN_INVOICING_KEY")); v != "" {
		return v
	}
	if cfg.Notion != nil {
		return strings.TrimSpace(cfg.Notion.InvoicingKey)
	}
	return ""
}

// invoicingPDFURL is the legacy Heroku direct-PDF URL (for dry-run hints only).
func invoicingPDFURL(cfg *config.RuntimeConfig, pageID string) string {
	slug := strings.ReplaceAll(pageID, "-", "")
	base := resolveInvoicingBaseURL(cfg)
	key := resolveInvoicingKey(cfg)
	u := base + "/" + slug + ".pdf"
	if key != "" {
		u += "?key=" + key
	}
	return u
}

func invoicePDFFileName(invoiceNumber string) string {
	n := strings.TrimSpace(invoiceNumber)
	if n == "" {
		return "invoice.pdf"
	}
	return n + ".pdf"
}

func generateInvoicePDFBytes(ctx context.Context, cfg *config.RuntimeConfig, pageID string) ([]byte, error) {
	return invoicepdf.Generate(ctx, invoicepdf.Options{
		PageID:    pageID,
		RenderURL: resolveInvoicingBaseURL(cfg),
		Key:       resolveInvoicingKey(cfg),
	})
}

func attachInvoicePDF(cmd *cobra.Command, cfg *config.RuntimeConfig, nc *notion.Client, pageID, invoiceNumber string, dryRun bool, snap invoiceSnapshotOpts) error {
	fileName := invoicePDFFileName(invoiceNumber)

	if dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] would generate PDF for page %s and attach as %s\n", pageID, fileName)
		return nil
	}

	ctx := context.Background()
	pdf, err := generateInvoicePDFBytes(ctx, cfg, pageID)
	if err != nil {
		return err
	}

	line := &invoiceLine{InvoiceNumber: invoiceNumber, TargetPageID: pageID}
	saveInvoiceSnapshot(cmd.OutOrStdout(), cfg, nc, ctx, snap, "attach-pdf", line, pdf)

	if err := nc.SetInvoicePDFFromBytes(ctx, pageID, fileName, pdf); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "attached Invoice PDF (%s, %d bytes)\n", fileName, len(pdf))
	return nil
}

func runAttachInvoicePDF(cmd *cobra.Command, cfg *config.RuntimeConfig, invoiceNumber, notionToken string, dryRun bool) error {
	if cfg.Notion == nil {
		return fmt.Errorf("notion config required: add [notion] section to wrklogr.toml")
	}
	if strings.TrimSpace(invoiceNumber) == "" {
		return fmt.Errorf("--invoice-number is required")
	}

	token := resolveNotionAPIToken(cfg, notionToken)
	if token == "" {
		return fmt.Errorf("notion API token required")
	}

	nc := notion.NewClient(token, nil)
	ctx := context.Background()

	existing, err := nc.FindInvoiceByNumber(ctx, cfg.Notion.InvoiceDBID, invoiceNumber)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("invoice %s not found in Notion", invoiceNumber)
	}

	snap := invoiceSnapshotOpts{
		Enabled: !dryRun && !snapshotDisabled(cmd),
	}
	return attachInvoicePDF(cmd, cfg, nc, existing.PageID, invoiceNumber, dryRun, snap)
}

func snapshotDisabled(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Flags().Lookup("no-snapshot") == nil {
			continue
		}
		v, err := c.Flags().GetBool("no-snapshot")
		if err == nil {
			return v
		}
	}
	return false
}

func runPrintInvoicePDF(cmd *cobra.Command, cfg *config.RuntimeConfig, invoiceNumber, outputPath, notionToken string) error {
	if cfg.Notion == nil {
		return fmt.Errorf("notion config required")
	}
	if strings.TrimSpace(invoiceNumber) == "" {
		return fmt.Errorf("--invoice-number is required")
	}

	token := resolveNotionAPIToken(cfg, notionToken)
	if token == "" {
		return fmt.Errorf("notion API token required")
	}

	nc := notion.NewClient(token, nil)
	ctx := context.Background()

	existing, err := nc.FindInvoiceByNumber(ctx, cfg.Notion.InvoiceDBID, invoiceNumber)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("invoice %s not found", invoiceNumber)
	}

	if outputPath == "" {
		outputPath = invoicePDFFileName(invoiceNumber)
	}

	pdf, err := generateInvoicePDFBytes(ctx, cfg, existing.PageID)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outputPath, pdf, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outputPath, err)
	}
	abs, _ := filepath.Abs(outputPath)
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d bytes)\n", abs, len(pdf))
	return nil
}

func newNotionInvoiceAttachPDFCmd(getConfig func() (*config.RuntimeConfig, error)) *cobra.Command {
	var invoiceNumber string
	var notionToken string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "attach-pdf",
		Short: "Generate invoice PDF and attach to Notion",
		Long: `Renders the invoice PDF locally via tools/invoice-pdf (Puppeteer + bean-invoicing UI)
and uploads it to the Invoice PDF property on the Notion page.

Requires: Node.js, npm install in tools/invoice-pdf, and the invoicing web app
(Heroku or local _ext/bean-invoicing-web + API).

Env: BEAN_INVOICING_URL / notion.invoicing_url, BEAN_INVOICING_KEY / notion.invoicing_key`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := getConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			return runAttachInvoicePDF(cmd, cfg, invoiceNumber, notionToken, dryRun)
		},
	}

	cmd.Flags().StringVar(&invoiceNumber, "invoice-number", "", "Invoice number (e.g. ADV-805)")
	cmd.Flags().StringVar(&notionToken, "notion-token", "", "Notion API token")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print actions without generating or uploading")
	_ = cmd.MarkFlagRequired("invoice-number")

	return cmd
}

func newNotionInvoicePrintPDFCmd(getConfig func() (*config.RuntimeConfig, error)) *cobra.Command {
	var invoiceNumber string
	var outputPath string
	var notionToken string

	cmd := &cobra.Command{
		Use:   "print-pdf",
		Short: "Generate invoice PDF to a local file",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := getConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			return runPrintInvoicePDF(cmd, cfg, invoiceNumber, outputPath, notionToken)
		},
	}

	cmd.Flags().StringVar(&invoiceNumber, "invoice-number", "", "Invoice number")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output PDF path (default: ADV-XXX.pdf)")
	cmd.Flags().StringVar(&notionToken, "notion-token", "", "Notion API token")
	_ = cmd.MarkFlagRequired("invoice-number")

	return cmd
}
