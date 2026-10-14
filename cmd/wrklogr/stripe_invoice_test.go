package main

import (
	"testing"

	"github.com/bean-la/wrklogr/internal/config"
	"github.com/bean-la/wrklogr/internal/noko"
)

func TestAggregateStripeInvoiceDays(t *testing.T) {
	entries := []noko.Entry{
		{ID: 2, Date: "2026-09-03", Minutes: 120, Description: "site work"},
		{ID: 1, Date: "2026-09-03", Minutes: 120, Description: "site work"},
		{ID: 3, Date: "2026-09-03", Minutes: 60, Description: "client call"},
		{ID: 4, Date: "2026-09-02", Minutes: 60, Description: "planning"},
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
	if len(days[1].EntryIDs) != 3 || days[1].EntryIDs[0] != 1 || days[1].EntryIDs[2] != 3 {
		t.Fatalf("unexpected entry IDs: %#v", days[1].EntryIDs)
	}
}

func TestAggregateStripeInvoiceDaysRejectsInvalidRate(t *testing.T) {
	if _, err := aggregateStripeInvoiceDays(nil, 0); err == nil {
		t.Fatal("expected invalid rate error")
	}
}

func TestAggregateLegacyQuarterDay(t *testing.T) {
	entries := []noko.Entry{
		{ID: 2, Minutes: 120},
		{ID: 1, Minutes: 120},
		{ID: 3, Minutes: 90},
	}
	days, err := aggregateLegacyQuarterDay(entries, 850)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 {
		t.Fatalf("expected one summary line, got %#v", days)
	}
	// 5.5h / 8 = 0.6875 days, rounded to 0.75 days at $850/day.
	if days[0].Minutes != 330 || days[0].AmountCents != 63750 {
		t.Fatalf("unexpected legacy aggregation: %#v", days[0])
	}
	if len(days[0].EntryIDs) != 3 || days[0].EntryIDs[0] != 1 {
		t.Fatalf("unexpected entry IDs: %#v", days[0].EntryIDs)
	}
}

func TestAggregateLegacyQuarterDayCategoriesAndRates(t *testing.T) {
	legacy := &config.StripeLegacyConfig{
		DefaultCategory: "backend",
		Rates:           map[string]float64{"backend": 850, "design": 1000},
		Users:           map[string][]string{"backend": {"dev@example.com"}},
	}
	entries := []noko.Entry{
		{ID: 1, Minutes: 120, User: noko.User{Email: "dev@example.com"}, Tags: []noko.Tag{{Name: "development"}}},
		{ID: 2, Minutes: 120, User: noko.User{Email: "designer@example.com"}, Tags: []noko.Tag{{Name: "design"}}},
	}
	days, err := aggregateLegacyQuarterDayWithConfig(entries, 900, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 || days[0].Category != "backend" || days[1].Category != "design" {
		t.Fatalf("unexpected category lines: %#v", days)
	}
	if days[0].AmountCents != 21250 || days[1].AmountCents != 25000 {
		t.Fatalf("unexpected category amounts: %#v", days)
	}
}
