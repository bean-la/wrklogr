package config

import (
	"testing"
	"time"
)

func TestBuildRuntimeConfigDefaults(t *testing.T) {
	cfg, err := buildRuntimeConfig(Config{}, "")
	if err != nil {
		t.Fatalf("buildRuntimeConfig() error = %v", err)
	}

	if cfg.SessionGap != 2*time.Hour {
		t.Fatalf("SessionGap = %v, want %v", cfg.SessionGap, 2*time.Hour)
	}
	if cfg.Timezone.String() != time.Local.String() {
		t.Fatalf("Timezone = %v, want local timezone %v", cfg.Timezone, time.Local)
	}
}

func TestBuildRuntimeConfigRepoValidation(t *testing.T) {
	tests := []struct {
		name    string
		repos   []string
		wantErr bool
	}{
		{name: "valid", repos: []string{"owner/repo"}, wantErr: false},
		{name: "empty is ignored", repos: []string{" ", "owner/repo"}, wantErr: false},
		{name: "missing owner", repos: []string{"/repo"}, wantErr: true},
		{name: "missing repo", repos: []string{"owner/"}, wantErr: true},
		{name: "too many segments", repos: []string{"o/r/extra"}, wantErr: true},
		{name: "space in repo", repos: []string{"owner/my repo"}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildRuntimeConfig(Config{Repos: tc.repos}, "")
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}
