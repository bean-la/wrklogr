package noko

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const defaultBaseURL = "https://api.nokotime.com/v2"

type Client struct {
	httpClient *http.Client
	baseURL    string
	token      string
}

func NewClient(token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		httpClient: httpClient,
		baseURL:    defaultBaseURL,
		token:      token,
	}
}

type EntryRequest struct {
	Date        string `json:"date"`
	Minutes     int    `json:"minutes"`
	Description string `json:"description,omitempty"`
	ProjectID   int    `json:"project_id,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
	SourceURL   string `json:"source_url,omitempty"`
}

type Entry struct {
	ID        int    `json:"id"`
	Date      string `json:"date"`
	Minutes   int    `json:"minutes"`
	SourceURL string `json:"source_url"`
}

// ListEntries fetches all entries between from and to (YYYY-MM-DD), following pagination.
func (c *Client) ListEntries(ctx context.Context, from, to string) ([]Entry, error) {
	var all []Entry
	pageNum := 1
	for {
		params := url.Values{}
		params.Set("from", from)
		params.Set("to", to)
		params.Set("per_page", "100")
		params.Set("page", fmt.Sprintf("%d", pageNum))

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/entries?"+params.Encode(), nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("X-NokoToken", c.token)
		req.Header.Set("User-Agent", "wrklogr/1.0")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("do request: %w", err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read response: %w", readErr)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("noko API error (%d): %s", resp.StatusCode, string(body))
		}

		var batch []Entry
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("unmarshal entries: %w", err)
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
		pageNum++
	}
	return all, nil
}

func (c *Client) CreateEntry(ctx context.Context, entry EntryRequest) error {
	body, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal entry: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/entries", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "wrklogr/1.0")
	req.Header.Set("X-NokoToken", c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return fmt.Errorf("noko API error (%d): failed to read body: %w", resp.StatusCode, readErr)
	}

	if resp.StatusCode == http.StatusCreated {
		return nil
	}
	if resp.StatusCode == http.StatusBadRequest && strings.Contains(string(respBody), `"duplicate"`) {
		return ErrDuplicate
	}
	return fmt.Errorf("noko API error (%d): %s", resp.StatusCode, string(respBody))
}

// ErrDuplicate is returned by CreateEntry when Noko rejects the entry as a duplicate.
var ErrDuplicate = fmt.Errorf("noko: duplicate entry")
