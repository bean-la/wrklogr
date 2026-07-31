package invoicepdf

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindPrintScript(t *testing.T) {
	// From repo root when tests run via go test ./...
	_, err := FindPrintScript()
	if err != nil {
		// OK if not in repo; set WRKLOGR_ROOT for CI
		root := os.Getenv("WRKLOGR_ROOT")
		if root == "" {
			t.Skip("tools/invoice-pdf not found and WRKLOGR_ROOT unset")
		}
		t.Fatalf("FindPrintScript: %v", err)
	}

	root := t.TempDir()
	tools := filepath.Join(root, "tools", "invoice-pdf")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(tools, "print.mjs")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env node\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WRKLOGR_ROOT", root)

	got, err := FindPrintScript()
	if err != nil {
		t.Fatal(err)
	}
	if got != script {
		t.Fatalf("got %q want %q", got, script)
	}
}
