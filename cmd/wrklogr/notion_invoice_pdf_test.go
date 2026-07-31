package main

import (
	"testing"

	"github.com/bean-la/wrklogr/internal/config"
)

func TestInvoicingPDFURL(t *testing.T) {
	t.Parallel()

	cfg := &config.RuntimeConfig{
		Notion: &config.NotionConfig{
			InvoicingURL: "https://bean-invoicing.herokuapp.com",
			InvoicingKey: "test-key",
		},
	}
	got := invoicingPDFURL(cfg, "35fb1bbd-3064-809d-bb57-f9e7ff8b4c57")
	want := "https://bean-invoicing.herokuapp.com/35fb1bbd3064809dbb57f9e7ff8b4c57.pdf?key=test-key"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestInvoicePDFFileName(t *testing.T) {
	t.Parallel()
	if got := invoicePDFFileName("ADV-805"); got != "ADV-805.pdf" {
		t.Fatalf("got %q", got)
	}
}
