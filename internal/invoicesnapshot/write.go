package invoicesnapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FetchMeta records CLI inputs used to compute the invoice.
type FetchMeta struct {
	Since        string   `json:"since,omitempty"`
	Until        string   `json:"until,omitempty"`
	BilledFrom   string   `json:"billed_from,omitempty"`
	BilledTo     string   `json:"billed_to,omitempty"`
	MonthLabel   string   `json:"month_label,omitempty"`
	Author       string   `json:"author,omitempty"`
	RepoPatterns []string `json:"repo_patterns,omitempty"`
	LocalPaths   []string `json:"local_paths,omitempty"`
}

// ProposedInvoice is what wrklogr is about to write to Notion.
type ProposedInvoice struct {
	ClientName    string   `json:"client_name"`
	InvoiceNumber string   `json:"invoice_number"`
	Amount        float64  `json:"amount"`
	Hours         float64  `json:"hours"`
	Rate          float64  `json:"rate"`
	Role          string   `json:"role"`
	BilledFrom    string   `json:"billed_from"`
	BilledTo      string   `json:"billed_to"`
	NET           string   `json:"net,omitempty"`
	NetDays       int      `json:"net_days,omitempty"`
	Repos         []string `json:"repos,omitempty"`
	NokoDescs     []string `json:"noko_descs,omitempty"`
	Description   string   `json:"description,omitempty"`
	ClientPageID  string   `json:"client_page_id,omitempty"`
	TargetPageID  string   `json:"target_page_id,omitempty"`
}

// ProjectAgg is per-Noko-project hours used in the computation.
type ProjectAgg struct {
	ProjectID int      `json:"project_id"`
	Minutes   int      `json:"minutes"`
	Repos     []string `json:"repos,omitempty"`
	Messages  []string `json:"commit_messages,omitempty"`
	NokoDescs []string `json:"noko_descs,omitempty"`
}

// Payload is written as meta.json alongside optional sidecar files.
type Payload struct {
	SavedAt       time.Time        `json:"saved_at"`
	Action        string           `json:"action"`
	InvoiceNumber string           `json:"invoice_number"`
	PageID        string           `json:"page_id,omitempty"`
	Fetch         FetchMeta        `json:"fetch,omitempty"`
	Proposed      *ProposedInvoice `json:"proposed,omitempty"`
	Projects      []ProjectAgg     `json:"projects,omitempty"`
	NotionBefore  string           `json:"notion_before_file,omitempty"`
	PDFFile       string           `json:"pdf_file,omitempty"`
}

// Save writes a timestamped snapshot directory and returns its path.
func Save(baseDir string, p Payload, notionBefore []byte, pdf []byte) (string, error) {
	if strings.TrimSpace(baseDir) == "" {
		return "", fmt.Errorf("snapshot base dir is empty")
	}
	if strings.TrimSpace(p.InvoiceNumber) == "" {
		return "", fmt.Errorf("invoice number is required for snapshot")
	}
	if p.SavedAt.IsZero() {
		p.SavedAt = time.Now().UTC()
	}

	safeNum := sanitizePathSegment(p.InvoiceNumber)
	stamp := p.SavedAt.Format("20060102T150405Z")
	dirName := fmt.Sprintf("%s_%s_%s", safeNum, stamp, sanitizePathSegment(p.Action))
	dir := filepath.Join(baseDir, safeNum, dirName)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir snapshot: %w", err)
	}

	if len(notionBefore) > 0 {
		p.NotionBefore = "notion-before.json"
		if err := os.WriteFile(filepath.Join(dir, p.NotionBefore), notionBefore, 0o644); err != nil {
			return "", err
		}
	}
	if len(pdf) > 0 {
		p.PDFFile = "invoice.pdf"
		if err := os.WriteFile(filepath.Join(dir, p.PDFFile), pdf, 0o644); err != nil {
			return "", err
		}
	}

	meta, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o644); err != nil {
		return "", err
	}
	if p.Proposed != nil {
		prop, err := json.MarshalIndent(p.Proposed, "", "  ")
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, "proposed.json"), prop, 0o644); err != nil {
			return "", err
		}
	}
	if len(p.Projects) > 0 {
		proj, err := json.MarshalIndent(p.Projects, "", "  ")
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, "projects.json"), proj, 0o644); err != nil {
			return "", err
		}
	}

	return dir, nil
}

func sanitizePathSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
