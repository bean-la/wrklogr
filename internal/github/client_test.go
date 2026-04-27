package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type commitsRequest struct {
	page    string
	perPage string
	since   string
	until   string
}

func TestListCommitsPaginatesAndPassesDateFilters(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	until := time.Date(2026, time.January, 3, 18, 30, 0, 0, time.UTC)

	requests := make([]commitsRequest, 0, 2)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/bean-la/wrklogr/commits" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		q := r.URL.Query()
		req := commitsRequest{
			page:    q.Get("page"),
			perPage: q.Get("per_page"),
			since:   q.Get("since"),
			until:   q.Get("until"),
		}
		requests = append(requests, req)

		page := q.Get("page")
		if page == "" || page == "1" {
			addNextLink(t, w, serverURL(server.URL), "/repos/bean-la/wrklogr/commits?page=2&per_page=100")
			writeCommits(t, w, []string{"1111111"})
			return
		}

		if page == "2" {
			writeCommits(t, w, []string{"2222222"})
			return
		}

		t.Fatalf("unexpected page query: %q", page)
	}))
	defer server.Close()

	client := NewClient("", server.Client())
	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	client.api.BaseURL = baseURL

	commits, err := client.ListCommits(context.Background(), "bean", "wrklogr", &since, &until)
	if err != nil {
		t.Fatalf("ListCommits returned error: %v", err)
	}

	if len(commits) != 2 {
		t.Fatalf("expected 2 commits, got %d", len(commits))
	}

	if len(requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(requests))
	}

	for _, req := range requests {
		if req.perPage != "100" {
			t.Fatalf("expected per_page=100, got %q", req.perPage)
		}
		if req.since != since.Format(time.RFC3339) {
			t.Fatalf("expected since=%q, got %q", since.Format(time.RFC3339), req.since)
		}
		if req.until != until.Format(time.RFC3339) {
			t.Fatalf("expected until=%q, got %q", until.Format(time.RFC3339), req.until)
		}
	}
}

func TestGetViewerIdentityLoadsLoginAndEmails(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			if err := json.NewEncoder(w).Encode(map[string]any{"login": "BeanDev"}); err != nil {
				t.Fatalf("encode user response: %v", err)
			}
		case "/user/emails":
			payload := []map[string]any{
				{"email": "dev@example.com", "primary": true},
				{"email": "alias@example.com", "primary": false},
			}
			if err := json.NewEncoder(w).Encode(payload); err != nil {
				t.Fatalf("encode emails response: %v", err)
			}
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient("", server.Client())
	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	client.api.BaseURL = baseURL

	identity, err := client.GetViewerIdentity(context.Background())
	if err != nil {
		t.Fatalf("GetViewerIdentity returned error: %v", err)
	}
	if identity.Login != "beandev" {
		t.Fatalf("expected lower-cased login beandev, got %q", identity.Login)
	}
	if len(identity.Emails) != 2 {
		t.Fatalf("expected 2 emails, got %d", len(identity.Emails))
	}
	if _, ok := identity.Emails["dev@example.com"]; !ok {
		t.Fatalf("expected dev@example.com in identity emails")
	}
}

func writeCommits(t *testing.T, w http.ResponseWriter, shas []string) {
	t.Helper()

	resp := make([]map[string]string, 0, len(shas))
	for _, sha := range shas {
		resp = append(resp, map[string]string{"sha": sha})
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		t.Fatalf("encode commits response: %v", err)
	}
}

func addNextLink(t *testing.T, w http.ResponseWriter, base string, nextPath string) {
	t.Helper()
	w.Header().Set("Link", "<"+base+nextPath+`>; rel="next"`)
}

func serverURL(raw string) string {
	return strings.TrimSuffix(raw, "/")
}
