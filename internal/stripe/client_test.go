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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk_test_example" {
			t.Errorf("missing auth header: %q", r.Header.Get("Authorization"))
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
	if err := client.CreateInvoiceItem(context.Background(), CreateInvoiceItemParams{Customer: "cus_test", Invoice: invoice.ID, Amount: 90000, Currency: "usd", Description: "work"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != "/v1/invoices,/v1/invoiceitems" {
		t.Fatalf("unexpected paths: %v", paths)
	}
}
