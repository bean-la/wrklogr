package main

import (
	"testing"

	"github.com/bean-la/wrklogr/internal/noko"
)

func TestAggregateStripeInvoiceDays(t *testing.T) {
	entries := []noko.Entry{
		{Date: "2026-09-03", Minutes: 120, Description: "site work"},
		{Date: "2026-09-03", Minutes: 120, Description: "site work"},
		{Date: "2026-09-03", Minutes: 60, Description: "client call"},
		{Date: "2026-09-02", Minutes: 60, Description: "planning"},
		{Date: "2026-09-04", Minutes: 0, Description: "ignored"},
	}
	days, err := aggregateStripeInvoiceDays(entries, 900)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 || days[0].Date != "2026-09-02" || days[1].Date != "2026-09-03" {
		t.Fatalf("unexpected days: %#v", days)
	}
	if days[0].Minutes != 60 || days[0].AmountCents != 11250 {
		t.Fatalf("unexpected first day: %#v", days[0])
	}
	if days[1].Minutes != 300 || days[1].AmountCents != 56250 {
		t.Fatalf("unexpected second day: %#v", days[1])
	}
	if len(days[1].Descriptions) != 2 {
		t.Fatalf("expected deduplicated descriptions: %#v", days[1].Descriptions)
	}
}

func TestAggregateStripeInvoiceDaysRejectsInvalidRate(t *testing.T) {
	if _, err := aggregateStripeInvoiceDays(nil, 0); err == nil {
		t.Fatal("expected invalid rate error")
	}
}
