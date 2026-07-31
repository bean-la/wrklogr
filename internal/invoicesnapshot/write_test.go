package invoicesnapshot

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSave(t *testing.T) {
	dir := t.TempDir()
	payload := Payload{
		SavedAt:       time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
		Action:        "update",
		InvoiceNumber: "ADV-805",
		PageID:        "page-id",
		Proposed:      &ProposedInvoice{Amount: 4800, Hours: 32},
		Projects:      []ProjectAgg{{ProjectID: 1, Minutes: 1920}},
	}
	snapDir, err := Save(dir, payload, []byte(`{"id":"page-id"}`), []byte("%PDF"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"meta.json", "proposed.json", "projects.json", "notion-before.json", "invoice.pdf"} {
		if _, err := os.Stat(filepath.Join(snapDir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}

func TestSanitizePathSegment(t *testing.T) {
	if got := sanitizePathSegment("ADV-805/foo"); got != "ADV-805_foo" {
		t.Fatalf("got %q", got)
	}
}
