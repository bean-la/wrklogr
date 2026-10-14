package stripe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientCreatesInvoiceAndItem(t *testing.T) {
	var paths []string
	var itemMetadata string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk_test_example" {
			t.Errorf("missing auth header: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path == "/v1/invoiceitems" {
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse item form: %v", err)
			}
			itemMetadata = r.Form.Get("metadata[legacy_category]")
		}
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/v1/invoices" {
			_ = json.NewEncoder(w).Encode(Invoice{ID: "in_test", Status: "draft"})
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewClient("sk_test_example", server.Client())
	client.baseURL = server.URL + "/v1"
	invoice, err := client.CreateInvoice(context.Background(), CreateInvoiceParams{Customer: "cus_test", CollectionMethod: "send_invoice", DaysUntilDue: 30})
	if err != nil {
		t.Fatal(err)
	}
	if invoice.ID != "in_test" {
		t.Fatalf("unexpected invoice: %#v", invoice)
	}
	if err := client.CreateInvoiceItem(context.Background(), CreateInvoiceItemParams{Customer: "cus_test", Invoice: invoice.ID, Amount: 90000, Currency: "usd", Description: "work", Metadata: map[string]string{"legacy_category": "backend"}}); err != nil {
		t.Fatal(err)
	}
	if itemMetadata != "backend" {
		t.Fatalf("invoice item metadata = %q", itemMetadata)
	}
	if strings.Join(paths, ",") != "/v1/invoices,/v1/invoiceitems" {
		t.Fatalf("unexpected paths: %v", paths)
	}
}

func TestListInvoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/invoices" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if r.URL.Query().Get("customer") != "cus_test" || r.URL.Query().Get("status") != "draft" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"in_test","status":"draft","metadata":{"wrklogr_invoice_key":"noko-test"}}]}`))
	}))
	defer server.Close()

	client := NewClient("sk_test_example", server.Client())
	client.baseURL = server.URL + "/v1"
	invoices, err := client.ListInvoices(context.Background(), "cus_test", "draft")
	if err != nil {
		t.Fatal(err)
	}
	if len(invoices) != 1 || invoices[0].Metadata["wrklogr_invoice_key"] != "noko-test" {
		t.Fatalf("unexpected invoices: %#v", invoices)
	}
}
