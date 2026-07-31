package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/bean-la/wrklogr/internal/config"
	"github.com/bean-la/wrklogr/internal/invoicesnapshot"
	"github.com/bean-la/wrklogr/internal/notion"
)

type invoiceSnapshotOpts struct {
	Enabled   bool
	FetchMeta invoicesnapshot.FetchMeta
	Fetched   *notionInvoiceFetched
}

func proposedFromLine(line *invoiceLine) *invoicesnapshot.ProposedInvoice {
	if line == nil {
		return nil
	}
	return &invoicesnapshot.ProposedInvoice{
		ClientName:    line.ClientName,
		InvoiceNumber: line.InvoiceNumber,
		Amount:        line.Amount,
		Hours:         line.Hours,
		Rate:          line.Rate,
		Role:          line.Role,
		BilledFrom:    line.BilledFrom,
		BilledTo:      line.BilledTo,
		NET:           line.NET,
		NetDays:       line.NetDays,
		Repos:         line.Repos,
		NokoDescs:     line.NokoDescs,
		Description:   line.Desc,
		ClientPageID:  line.ClientPageID,
		TargetPageID:  line.TargetPageID,
	}
}

func projectsFromAggs(aggs map[int]*projectAgg) []invoicesnapshot.ProjectAgg {
	if len(aggs) == 0 {
		return nil
	}
	ids := make([]int, 0, len(aggs))
	for id := range aggs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := make([]invoicesnapshot.ProjectAgg, 0, len(ids))
	for _, id := range ids {
		a := aggs[id]
		if a == nil {
			continue
		}
		out = append(out, invoicesnapshot.ProjectAgg{
			ProjectID: id,
			Minutes:   a.Minutes,
			Repos:     sortedRepos(a),
			Messages:  append([]string(nil), a.Msgs...),
			NokoDescs: append([]string(nil), a.NokoDescs...),
		})
	}
	return out
}

func fetchNotionPage(ctx context.Context, nc *notion.Client, pageID string) []byte {
	if nc == nil || pageID == "" {
		return nil
	}
	raw, err := nc.GetPage(ctx, pageID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: snapshot notion page %s: %v\n", pageID, err)
		return nil
	}
	return raw
}

func saveInvoiceSnapshot(
	out io.Writer,
	cfg *config.RuntimeConfig,
	nc *notion.Client,
	ctx context.Context,
	snap invoiceSnapshotOpts,
	action string,
	line *invoiceLine,
	pdf []byte,
) {
	if !snap.Enabled || line == nil {
		return
	}
	pageID := line.TargetPageID
	var notionBefore []byte
	if pageID != "" {
		notionBefore = fetchNotionPage(ctx, nc, pageID)
	}
	payload := invoicesnapshot.Payload{
		Action:        action,
		InvoiceNumber: line.InvoiceNumber,
		PageID:        pageID,
		Fetch:         snap.FetchMeta,
		Proposed:      proposedFromLine(line),
	}
	if snap.Fetched != nil {
		payload.Projects = projectsFromAggs(snap.Fetched.Aggs)
	}
	dir, err := invoicesnapshot.Save(invoicesnapshot.ResolveDir(cfg), payload, notionBefore, pdf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: invoice snapshot: %v\n", err)
		return
	}
	if out != nil {
		fmt.Fprintf(out, "snapshot saved: %s\n", dir)
	}
}
