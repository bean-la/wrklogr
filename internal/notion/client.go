package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const apiBase = "https://api.notion.com/v1"
const apiVersion = "2022-06-28"

// Client is a minimal Notion API client.
type Client struct {
	http  *http.Client
	token string
}

// NewClient creates a new Notion API client.
func NewClient(token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{http: httpClient, token: token}
}

// ClientRecord holds the fields we read from the Clients database.
type ClientRecord struct {
	PageID           string
	Name             string
	NokoProjectID    int
	BackendRate      float64
	FrontendRate     float64
	DesignRate       float64
	SrBackendRate    float64
	IOSRate          float64
	NET              string
	NetDays          int
	InvoicingContact string
}

// RateForRole returns the daily rate for the given role string.
// Unrecognised roles fall back to the backend rate.
func (r *ClientRecord) RateForRole(role string) float64 {
	switch strings.ToLower(role) {
	case "frontend":
		return r.FrontendRate
	case "design":
		return r.DesignRate
	case "sr_backend", "sr-backend":
		return r.SrBackendRate
	case "ios":
		return r.IOSRate
	default:
		return r.BackendRate
	}
}

// InvoiceRequest holds the fields needed to create a draft invoice page.
type InvoiceRequest struct {
	InvoiceNumber string
	Amount        float64
	BilledFrom    string // YYYY-MM-DD
	BilledTo      string // YYYY-MM-DD
	ClientPageID  string
	Description   string
	NET           string
	NetDays       int
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, r)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Notion-Version", apiVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	data, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read response: %w", readErr)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("notion %s %s: %d: %s", method, path, resp.StatusCode, string(data))
	}
	return data, nil
}

// FindClientByNokoProjectID queries the Clients DB for the client whose
// "Noko Project ID" property matches projectID. Returns nil, nil when not found.
func (c *Client) FindClientByNokoProjectID(ctx context.Context, dbID string, projectID int) (*ClientRecord, error) {
	body := map[string]any{
		"filter": map[string]any{
			"property": "Noko Project ID",
			"number":   map[string]any{"equals": projectID},
		},
	}
	data, err := c.do(ctx, "POST", "/databases/"+dbID+"/query", body)
	if err != nil {
		return nil, err
	}
	var result struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("unmarshal query: %w", err)
	}
	if len(result.Results) == 0 {
		return nil, nil
	}
	return parseClientPage(result.Results[0])
}

// InvoiceRecord holds the fields we read from an existing Invoice Status page.
type InvoiceRecord struct {
	PageID       string
	ClientPageID string
	Amount       float64
}

// GetPage returns the raw Notion page JSON for a page id.
func (c *Client) GetPage(ctx context.Context, pageID string) ([]byte, error) {
	if strings.TrimSpace(pageID) == "" {
		return nil, fmt.Errorf("page id is required")
	}
	return c.do(ctx, http.MethodGet, "/pages/"+pageID, nil)
}

// FindInvoiceByNumber queries the Invoice Status DB for the page with the given invoice number.
// Returns nil, nil if not found.
func (c *Client) FindInvoiceByNumber(ctx context.Context, dbID, number string) (*InvoiceRecord, error) {
	body := map[string]any{
		"filter": map[string]any{
			"property": "Invoice Number",
			"title":    map[string]any{"equals": number},
		},
	}
	data, err := c.do(ctx, "POST", "/databases/"+dbID+"/query", body)
	if err != nil {
		return nil, err
	}
	var result struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("unmarshal query: %w", err)
	}
	if len(result.Results) == 0 {
		return nil, nil
	}
	return parseInvoicePage(result.Results[0])
}

// GetClientPage retrieves a client record from its Notion page ID.
func (c *Client) GetClientPage(ctx context.Context, pageID string) (*ClientRecord, error) {
	data, err := c.do(ctx, "GET", "/pages/"+pageID, nil)
	if err != nil {
		return nil, err
	}
	var page map[string]any
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, fmt.Errorf("unmarshal page: %w", err)
	}
	return parseClientPage(page)
}

// UpdateInvoiceDescription patches only the Description property.
func (c *Client) UpdateInvoiceDescription(ctx context.Context, pageID, description string) error {
	if pageID == "" {
		return fmt.Errorf("page id is required")
	}
	if strings.TrimSpace(description) == "" {
		return fmt.Errorf("description is required")
	}
	props := map[string]any{
		"Description": richTextPropValue(description),
	}
	_, err := c.do(ctx, http.MethodPatch, "/pages/"+pageID, map[string]any{"properties": props})
	return err
}

// UpdateInvoice patches an existing invoice page with the computed fields.
// It does not touch Invoice Number, Client, or Invoice Status.
func (c *Client) UpdateInvoice(ctx context.Context, pageID string, inv InvoiceRequest) error {
	props := map[string]any{
		"Amount":       map[string]any{"number": inv.Amount},
		"Billed Dates": map[string]any{"date": map[string]any{"start": inv.BilledFrom, "end": inv.BilledTo}},
	}
	if inv.Description != "" {
		props["Description"] = richTextPropValue(inv.Description)
	}
	// Net (rollup) and Net Days (formula) are read-only computed properties —
	// derived from the Client relation; cannot be written via the API.
	_, err := c.do(ctx, "PATCH", "/pages/"+pageID, map[string]any{"properties": props})
	return err
}

// CreateInvoice creates a new draft invoice page in the Invoice Status DB.
// Returns the created page ID.
func (c *Client) CreateInvoice(ctx context.Context, dbID string, inv InvoiceRequest) (string, error) {
	props := map[string]any{
		"Invoice Number": titlePropValue(inv.InvoiceNumber),
		"Amount":         map[string]any{"number": inv.Amount},
		"Billed Dates":   map[string]any{"date": map[string]any{"start": inv.BilledFrom, "end": inv.BilledTo}},
		// Invoice Status is a status-type property in the DB (not select).
		"Invoice Status": map[string]any{"status": map[string]any{"name": "Draft"}},
	}
	if inv.Description != "" {
		props["Description"] = richTextPropValue(inv.Description)
	}
	if inv.ClientPageID != "" {
		props["Client"] = map[string]any{
			"relation": []any{map[string]any{"id": inv.ClientPageID}},
		}
	}
	// Net is a rollup property (computed from the Client relation) — read-only,
	// cannot be written via the API. The rollup derives from the linked client.
	_ = inv.NET
	if inv.NetDays > 0 {
		props["Net Days"] = map[string]any{"number": inv.NetDays}
	}

	body := map[string]any{
		"parent":     map[string]any{"database_id": dbID},
		"properties": props,
	}
	data, err := c.do(ctx, "POST", "/pages", body)
	if err != nil {
		return "", err
	}
	var page struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return "", fmt.Errorf("unmarshal page response: %w", err)
	}
	return page.ID, nil
}

func parseInvoicePage(page map[string]any) (*InvoiceRecord, error) {
	id, _ := page["id"].(string)
	props, _ := page["properties"].(map[string]any)
	rec := &InvoiceRecord{PageID: id}
	if props != nil {
		rec.Amount = numProp(props, "Amount")
		if clientProp, ok := props["Client"].(map[string]any); ok {
			if rel, ok := clientProp["relation"].([]any); ok && len(rel) > 0 {
				if item, ok := rel[0].(map[string]any); ok {
					rec.ClientPageID, _ = item["id"].(string)
				}
			}
		}
	}
	return rec, nil
}

func parseClientPage(page map[string]any) (*ClientRecord, error) {
	id, _ := page["id"].(string)
	props, _ := page["properties"].(map[string]any)
	if props == nil {
		return nil, fmt.Errorf("missing properties on client page %s", id)
	}
	rec := &ClientRecord{PageID: id}
	rec.Name = titleText(props, "name")
	rec.NokoProjectID = int(numProp(props, "Noko Project ID"))
	rec.BackendRate = numProp(props, "Backend Dev Rate")
	rec.FrontendRate = numProp(props, "Frontend Dev Rate")
	rec.DesignRate = numProp(props, "Design Rate")
	rec.SrBackendRate = numProp(props, "Sr. Backend Rate")
	rec.IOSRate = numProp(props, "iOS Dev Rate")
	rec.NET = selProp(props, "NET")
	if after, ok := strings.CutPrefix(rec.NET, "NET-"); ok {
		if n, err := strconv.Atoi(after); err == nil {
			rec.NetDays = n
		}
	}
	contact := urlOrEmailProp(props, "Invoicing Contact")
	rec.InvoicingContact = strings.TrimPrefix(contact, "mailto:")
	return rec, nil
}

func numProp(props map[string]any, key string) float64 {
	v, _ := props[key].(map[string]any)
	n, _ := v["number"].(float64)
	return n
}

func selProp(props map[string]any, key string) string {
	v, _ := props[key].(map[string]any)
	sel, _ := v["select"].(map[string]any)
	name, _ := sel["name"].(string)
	return name
}

func titleText(props map[string]any, key string) string {
	v, _ := props[key].(map[string]any)
	arr, _ := v["title"].([]any)
	if len(arr) == 0 {
		return ""
	}
	item, _ := arr[0].(map[string]any)
	text, _ := item["plain_text"].(string)
	return text
}

func urlOrEmailProp(props map[string]any, key string) string {
	v, _ := props[key].(map[string]any)
	if u, ok := v["url"].(string); ok && u != "" {
		return u
	}
	if e, ok := v["email"].(string); ok && e != "" {
		return e
	}
	return ""
}

func titlePropValue(text string) map[string]any {
	return map[string]any{
		"title": []any{map[string]any{"text": map[string]any{"content": text}}},
	}
}

func richTextPropValue(text string) map[string]any {
	return map[string]any{
		"rich_text": []any{map[string]any{"text": map[string]any{"content": text}}},
	}
}
