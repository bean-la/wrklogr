package main

import (
	"testing"
	"time"
)

func TestParseDateBound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		endOfDay bool
		wantNil  bool
		want     time.Time
		wantErr  bool
	}{
		{
			name:     "empty",
			input:    "",
			endOfDay: false,
			wantNil:  true,
		},
		{
			name:     "rfc3339",
			input:    "2026-01-01T12:34:56Z",
			endOfDay: false,
			want:     time.Date(2026, time.January, 1, 12, 34, 56, 0, time.UTC),
		},
		{
			name:     "day start",
			input:    "2026-01-01",
			endOfDay: false,
			want:     time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:     "day end",
			input:    "2026-01-01",
			endOfDay: true,
			want:     time.Date(2026, time.January, 1, 23, 59, 59, 0, time.UTC),
		},
		{
			name:     "invalid",
			input:    "2026/01/01",
			endOfDay: false,
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDateBound(tc.input, tc.endOfDay)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDateBound() error = %v", err)
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil time, got %v", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected non-nil time")
			}
			if !got.Equal(tc.want) {
				t.Fatalf("got %v, want %v", *got, tc.want)
			}
		})
	}
}

func TestSplitRepo(t *testing.T) {
	t.Parallel()

	owner, repo, err := splitRepo("bean/wrklogr")
	if err != nil {
		t.Fatalf("splitRepo() error = %v", err)
	}
	if owner != "bean" || repo != "wrklogr" {
		t.Fatalf("got owner/repo %s/%s, want bean/wrklogr", owner, repo)
	}

	if _, _, err := splitRepo("bean"); err == nil {
		t.Fatalf("expected error for invalid repo format")
	}
}
