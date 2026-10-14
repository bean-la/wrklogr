package stripe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

const defaultBaseURL = "https://api.stripe.com/v1"

type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
}

func NewClient(apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{httpClient: httpClient, baseURL: defaultBaseURL, apiKey: apiKey}
}

type Invoice struct {
	ID               string            `json:"id"`
	Status           string            `json:"status"`
	AmountDue        int64             `json:"amount_due"`
	HostedInvoiceURL string            `json:"hosted_invoice_url"`
	Metadata         map[string]string `json:"metadata"`
}

type invoiceList struct {
	Data []Invoice `json:"data"`
}

type CreateInvoiceParams struct {
	Customer         string
	Description      string
	DaysUntilDue     int
	CollectionMethod string
	Metadata         map[string]string
}

func (c *Client) CreateInvoice(ctx context.Context, p CreateInvoiceParams) (*Invoice, error) {
	form := url.Values{}
	form.Set("customer", p.Customer)
	form.Set("collection_method", p.CollectionMethod)
	form.Set("auto_advance", "false")
	if p.Description != "" {
		form.Set("description", p.Description)
	}
	if p.DaysUntilDue > 0 {
		form.Set("days_until_due", strconv.Itoa(p.DaysUntilDue))
	}
	for key, value := range p.Metadata {
		form.Set("metadata["+key+"]", value)
	}
	var out Invoice
	if err := c.do(ctx, http.MethodPost, "/invoices", form, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListInvoices returns invoices for a customer and status. It is used to make
// repeated draft creation for the same Noko period detectable.
func (c *Client) ListInvoices(ctx context.Context, customer, status string) ([]Invoice, error) {
	params := url.Values{}
	params.Set("customer", customer)
	params.Set("limit", "100")
	if status != "" {
		params.Set("status", status)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/invoices?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("create Stripe request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("User-Agent", "wrklogr/stripe-invoice")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Stripe request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read Stripe response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Stripe API error (%d): %s", resp.StatusCode, string(body))
	}
	var out invoiceList
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode Stripe response: %w", err)
	}
	return out.Data, nil
}

type CreateInvoiceItemParams struct {
	Customer    string
	Invoice     string
	Amount      int64
	Currency    string
	Description string
}

func (c *Client) CreateInvoiceItem(ctx context.Context, p CreateInvoiceItemParams) error {
	form := url.Values{}
	form.Set("customer", p.Customer)
	form.Set("invoice", p.Invoice)
	form.Set("amount", strconv.FormatInt(p.Amount, 10))
	form.Set("currency", p.Currency)
	form.Set("description", p.Description)
	return c.do(ctx, http.MethodPost, "/invoiceitems", form, &struct{}{})
}

func (c *Client) do(ctx context.Context, method, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return fmt.Errorf("create Stripe request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "wrklogr/stripe-invoice")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("Stripe request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read Stripe response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Stripe API error (%d): %s", resp.StatusCode, string(body))
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode Stripe response: %w", err)
		}
	}
	return nil
}
