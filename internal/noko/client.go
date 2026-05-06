package noko

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	if resp.StatusCode != http.StatusCreated {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("noko API error (%d): failed to read body: %w", resp.StatusCode, readErr)
		}
		return fmt.Errorf("noko API error (%d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}
