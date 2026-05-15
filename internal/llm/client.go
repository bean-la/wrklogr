package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	APIKey  string
	BaseURL string
	Model   string
}

type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	model      string
	cache      map[string]string
}

func NewClient(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-4o-mini"
	}
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    cfg.BaseURL,
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		cache:      make(map[string]string),
	}
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model     string    `json:"model"`
	Messages  []message `json:"messages"`
	MaxTokens int       `json:"max_tokens,omitempty"`
}

type choice struct {
	Message message `json:"message"`
}

type chatResponse struct {
	Choices []choice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *Client) Summarize(commits []string) (string, error) {
	key := strings.Join(commits, "\x00")
	if cached, ok := c.cache[key]; ok {
		return cached, nil
	}

	prompt := buildPrompt(commits)
	req := chatRequest{
		Model: c.model,
		Messages: []message{
			{Role: "system", Content: "You summarize work sessions from git commits. Write 1-2 concise sentences about what was accomplished. Focus on outcomes, not implementation details. Do not start with phrases like 'The session focused on' or 'The work session'. Start directly with what was done. Do not list individual commits."},
			{Role: "user", Content: prompt},
		},
		MaxTokens: 150,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("api call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if chatResp.Error != nil {
		return "", fmt.Errorf("api error: %s", chatResp.Error.Message)
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("no choices returned")
	}

	summary := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	c.cache[key] = summary
	return summary, nil
}

// SummarizeForInvoice generates a 1-2 sentence invoice description from a mix of
// Noko log entries and commit messages.
func (c *Client) SummarizeForInvoice(items []string) (string, error) {
	key := "invoice:" + strings.Join(items, "\x00")
	if cached, ok := c.cache[key]; ok {
		return cached, nil
	}

	var sb strings.Builder
	sb.WriteString("Write a 1-sentence invoice description for the following work:\n\n")
	for i, item := range items {
		if i >= 30 {
			sb.WriteString(fmt.Sprintf("... and %d more items\n", len(items)-30))
			break
		}
		line := strings.SplitN(item, "\n", 2)[0]
		// Strip trailing [repo (N commits)] tags added by wrklogr
		if idx := strings.LastIndex(line, " ["); idx != -1 && strings.HasSuffix(line, "commits)]") {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}
		sb.WriteString("- " + line + "\n")
	}
	sb.WriteString("\nDescription:")

	req := chatRequest{
		Model: c.model,
		Messages: []message{
			{Role: "system", Content: "You write professional invoice descriptions for freelance software development. Write exactly 1 short sentence (under 120 characters) summarizing what was delivered. Focus on the most significant outcome. Do not mention commits, logs, repos, or internal tools. Start directly with what was accomplished."},
			{Role: "user", Content: sb.String()},
		},
		MaxTokens: 60,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("api call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if chatResp.Error != nil {
		return "", fmt.Errorf("api error: %s", chatResp.Error.Message)
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("no choices returned")
	}

	summary := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	c.cache[key] = summary
	return summary, nil
}

func buildPrompt(commits []string) string {
	var sb strings.Builder
	sb.WriteString("Summarize this work session from the following commits:\n\n")
	for i, c := range commits {
		if i >= 20 {
			sb.WriteString(fmt.Sprintf("... and %d more commits\n", len(commits)-20))
			break
		}
		msg := strings.SplitN(c, "\n", 2)[0]
		msg = strings.TrimSpace(msg)
		if msg == "" {
			continue
		}
		sb.WriteString("- " + msg + "\n")
	}
	sb.WriteString("\nSummary:")
	return sb.String()
}
