package main

import (
	"testing"

	"github.com/bean-la/wrklogr/internal/config"
	"github.com/bean-la/wrklogr/internal/notion"
)

func TestResolveUpdateAggUsesClientProjectOnly(t *testing.T) {
	t.Parallel()

	client := &notion.ClientRecord{NokoProjectID: 586501}
	aggs := map[int]*projectAgg{
		586501: {Minutes: 60, NokoDescs: []string{"dublab only"}},
		708823: {Minutes: 2400, NokoDescs: []string{"third eye"}},
	}
	agg := resolveUpdateAgg(client, aggs)
	if len(agg.NokoDescs) != 1 || agg.NokoDescs[0] != "dublab only" {
		t.Fatalf("got noko descs %#v, want dublab only", agg.NokoDescs)
	}
	if agg.Minutes != 60 {
		t.Fatalf("got %d minutes, want 60", agg.Minutes)
	}
}

func TestResolveUpdateAggEmptyClientProjectDoesNotMergeOthers(t *testing.T) {
	t.Parallel()

	client := &notion.ClientRecord{NokoProjectID: 999999}
	aggs := map[int]*projectAgg{
		708823: {Minutes: 2400, NokoDescs: []string{"third eye"}},
	}
	agg := resolveUpdateAgg(client, aggs)
	if len(agg.NokoDescs) != 0 {
		t.Fatalf("expected empty noko descs, got %#v", agg.NokoDescs)
	}
	if agg.Minutes != 0 {
		t.Fatalf("expected 0 minutes, got %d", agg.Minutes)
	}
}

func TestResolveUpdateAggSingleProjectWithoutClient(t *testing.T) {
	t.Parallel()

	aggs := map[int]*projectAgg{
		586501: {Minutes: 60, NokoDescs: []string{"only"}},
	}
	agg := resolveUpdateAgg(nil, aggs)
	if agg.NokoDescs[0] != "only" {
		t.Fatalf("got %#v", agg.NokoDescs)
	}
}

func TestMergeProjectAggs(t *testing.T) {
	t.Parallel()

	nc := &config.NokoConfig{}
	_ = nc
	aggs := map[int]*projectAgg{
		1: {Minutes: 30, NokoDescs: []string{"a"}},
		2: {Minutes: 30, NokoDescs: []string{"b"}},
	}
	merged := mergeProjectAggs(aggs)
	if merged.Minutes != 60 || len(merged.NokoDescs) != 2 {
		t.Fatalf("got minutes=%d descs=%#v", merged.Minutes, merged.NokoDescs)
	}
}
