package noko

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateEntrySendsCorrectRequest(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/entries" {
			t.Fatalf("expected /entries, got %s", r.URL.Path)
		}

		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("X-NokoToken") != "test-token" {
			t.Fatalf("expected X-NokoToken test-token, got %s", r.Header.Get("X-NokoToken"))
		}
		if r.Header.Get("User-Agent") != "wrklogr/1.0" {
			t.Fatalf("expected User-Agent wrklogr/1.0, got %s", r.Header.Get("User-Agent"))
		}

		var body EntryRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Date != "2026-04-25" {
			t.Fatalf("expected date 2026-04-25, got %q", body.Date)
		}
		if body.Minutes != 120 {
			t.Fatalf("expected minutes 120, got %d", body.Minutes)
		}
		if body.Description != "test description" {
			t.Fatalf("expected description 'test description', got %q", body.Description)
		}
		if body.ProjectID != 42 {
			t.Fatalf("expected project_id 42, got %d", body.ProjectID)
		}

		w.Header().Set("Location", "https://api.nokotime.com/v2/entries/1")
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(map[string]any{"id": 1}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewClient("test-token", server.Client())
	client.baseURL = server.URL

	err := client.CreateEntry(context.Background(), EntryRequest{
		Date:        "2026-04-25",
		Minutes:     120,
		Description: "test description",
		ProjectID:   42,
	})
	if err != nil {
		t.Fatalf("CreateEntry returned error: %v", err)
	}
}

func TestCreateEntryHandlesAPIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		body, _ := json.Marshal(map[string]string{"message": "Invalid JSON"})
		_, _ = w.Write(body)
	}))
	defer server.Close()

	client := NewClient("test-token", server.Client())
	client.baseURL = server.URL

	err := client.CreateEntry(context.Background(), EntryRequest{
		Date:    "2026-04-25",
		Minutes: 60,
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected error to contain 400, got %q", err.Error())
	}
}

func TestCreateEntryRejectsUnauthorized(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		body, _ := json.Marshal(map[string]string{"message": "Forbidden"})
		_, _ = w.Write(body)
	}))
	defer server.Close()

	client := NewClient("bad-token", server.Client())
	client.baseURL = server.URL

	err := client.CreateEntry(context.Background(), EntryRequest{
		Date:    "2026-04-25",
		Minutes: 30,
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected error to contain 403, got %q", err.Error())
	}
}

func TestNewClientDefaults(t *testing.T) {
	t.Parallel()

	client := NewClient("token", nil)
	if client.token != "token" {
		t.Fatalf("expected token 'token', got %q", client.token)
	}
	if client.baseURL != defaultBaseURL {
		t.Fatalf("expected baseURL %q, got %q", defaultBaseURL, client.baseURL)
	}
	if client.httpClient == nil {
		t.Fatal("expected non-nil httpClient")
	}
}
