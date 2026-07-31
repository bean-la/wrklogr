package invoicesnapshot

import (
	"os"
	"path/filepath"

	"github.com/bean-la/wrklogr/internal/config"
)

// ResolveDir returns the base directory for invoice snapshots.
// Priority: WRKLOGR_INVOICE_SNAPSHOT_DIR env, notion.invoice_snapshot_dir, ~/.wrklogr/invoice-snapshots.
func ResolveDir(cfg *config.RuntimeConfig) string {
	if v := os.Getenv("WRKLOGR_INVOICE_SNAPSHOT_DIR"); v != "" {
		return v
	}
	if cfg != nil && cfg.Notion != nil {
		if v := cfg.Notion.InvoiceSnapshotDir; v != "" {
			return v
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".wrklogr/invoice-snapshots"
	}
	return filepath.Join(home, ".wrklogr", "invoice-snapshots")
}
