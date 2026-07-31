package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
)

// FetchPDF downloads a PDF from a public URL (e.g. bean-invoicing).
func FetchPDF(ctx context.Context, httpClient *http.Client, pdfURL string) ([]byte, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pdfURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/pdf")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch pdf: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch pdf %s: %d: %s", pdfURL, resp.StatusCode, string(data))
	}
	return data, nil
}

// UploadFile uploads PDF bytes to Notion and returns the file_upload id.
func (c *Client) UploadFile(ctx context.Context, fileName, contentType string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("empty file")
	}
	if contentType == "" {
		contentType = "application/pdf"
	}
	if fileName == "" {
		fileName = "invoice.pdf"
	}

	createBody := map[string]any{
		"filename":     fileName,
		"content_type": contentType,
	}
	raw, err := c.do(ctx, http.MethodPost, "/file_uploads", createBody)
	if err != nil {
		return "", fmt.Errorf("create file upload: %w", err)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		return "", fmt.Errorf("unmarshal file upload: %w", err)
	}
	if created.ID == "" {
		return "", fmt.Errorf("missing file upload id")
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, fileName))
	partHeader.Set("Content-Type", contentType)
	part, err := w.CreatePart(partHeader)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/file_uploads/"+created.ID+"/send", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Notion-Version", apiVersion)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("send file upload: %w", err)
	}
	respData, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return "", readErr
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("notion POST /file_uploads/%s/send: %d: %s", created.ID, resp.StatusCode, string(respData))
	}
	return created.ID, nil
}

// SetInvoicePDFFromBytes uploads PDF bytes and attaches them to the Invoice PDF property.
func (c *Client) SetInvoicePDFFromBytes(ctx context.Context, pageID, fileName string, pdf []byte) error {
	if pageID == "" {
		return fmt.Errorf("page id is required")
	}
	if len(pdf) == 0 {
		return fmt.Errorf("empty pdf")
	}
	if fileName == "" {
		fileName = "invoice.pdf"
	}

	uploadID, err := c.UploadFile(ctx, fileName, "application/pdf", pdf)
	if err != nil {
		return err
	}

	props := map[string]any{
		"Invoice PDF": map[string]any{
			"files": []any{
				map[string]any{
					"name": fileName,
					"type": "file_upload",
					"file_upload": map[string]any{
						"id": uploadID,
					},
				},
			},
		},
	}
	_, err = c.do(ctx, http.MethodPatch, "/pages/"+pageID, map[string]any{"properties": props})
	if err != nil {
		return fmt.Errorf("set invoice pdf: %w", err)
	}
	return nil
}

// SetInvoicePDF downloads a PDF from a URL and attaches it (legacy / fallback).
func (c *Client) SetInvoicePDF(ctx context.Context, pageID, pdfURL, fileName string) error {
	pdf, err := FetchPDF(ctx, c.http, pdfURL)
	if err != nil {
		return err
	}
	return c.SetInvoicePDFFromBytes(ctx, pageID, fileName, pdf)
}
