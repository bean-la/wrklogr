package invoicepdf

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Options configures local PDF generation via tools/invoice-pdf.
type Options struct {
	PageID    string
	RenderURL string
	Key       string
	OutPath   string // if empty, uses a temp file
}

// FindPrintScript locates tools/invoice-pdf/print.mjs relative to repo root or WRKLOGR_ROOT.
func FindPrintScript() (string, error) {
	if root := strings.TrimSpace(os.Getenv("WRKLOGR_ROOT")); root != "" {
		p := filepath.Join(root, "tools", "invoice-pdf", "print.mjs")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(dir, "tools", "invoice-pdf", "print.mjs")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("tools/invoice-pdf/print.mjs not found (set WRKLOGR_ROOT or run from wrklogr repo); run: cd tools/invoice-pdf && npm install")
}

// Generate renders an invoice PDF for a Notion page id.
func Generate(ctx context.Context, opts Options) ([]byte, error) {
	if strings.TrimSpace(opts.PageID) == "" {
		return nil, fmt.Errorf("page id is required")
	}

	script, err := FindPrintScript()
	if err != nil {
		return nil, err
	}

	outPath := opts.OutPath
	if outPath == "" {
		f, err := os.CreateTemp("", "wrklogr-invoice-*.pdf")
		if err != nil {
			return nil, err
		}
		outPath = f.Name()
		f.Close()
		defer os.Remove(outPath)
	}

	cmd := exec.CommandContext(ctx, "node", script, "--page-id", opts.PageID, "-o", outPath)
	cmd.Dir = filepath.Dir(script)
	cmd.Env = os.Environ()
	if opts.RenderURL != "" {
		cmd.Env = append(cmd.Env, "BEAN_INVOICING_RENDER_URL="+opts.RenderURL)
	}
	if opts.Key != "" {
		cmd.Env = append(cmd.Env, "BEAN_INVOICING_KEY="+opts.Key)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("invoice pdf: %w: %s", err, strings.TrimSpace(string(out)))
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("read pdf: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("invoice pdf: empty output")
	}
	return data, nil
}
