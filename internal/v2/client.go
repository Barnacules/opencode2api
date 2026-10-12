// Package v2 provides a client for OpenCode's V2 server API.
// The V2 server provides session-based access to OpenCode models,
// including free models that are blocked on the direct Zen API
// with FreeTierError.
package v2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client communicates with an OpenCode V2 server.
type Client struct {
	baseURL         string
	password        string
	httpClient      *http.Client
	disableBuildAgent bool
	
	// Session management
	mu            sync.Mutex
	sessionID     string
	sessionExpiry time.Time
	
	// Model list cache
	muModels sync.Mutex
	models   []ModelInfo
}

// ModelInfo describes a model returned by the V2 server.
type ModelInfo struct {
	ModelID    string `json:"modelID"`
	ProviderID string `json:"providerID"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Status     string `json:"status"`
}

// ChatMessage represents a message in the conversation.
type ChatMessage struct {
	Type    string         `json:"type"`
	Fill    string         `json:"fill,omitempty"`
	Finish  string         `json:"finish,omitempty"`
	Content []ContentBlock `json:"content,omitempty"`
}

// ContentBlock represents content in an assistant response.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Name string `json:"name,omitempty"`
	ID   string `json:"id,omitempty"`
	Args json.RawMessage `json:"arguments,omitempty"`
}

// ToolCallResult is what the client sees when the model requests tools.
type ToolCallResult struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction is the function details for a tool call.
type ToolCallFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// V2Response is the response from the V2 server's session API.
type V2Response struct {
	Text   string           `json:"text"`
	Tools  []ToolCallResult `json:"tools"`
	Finish string           `json:"finish"`
	Usage  *V2Usage         `json:"usage"`
}

// V2Usage contains token usage information.
type V2Usage struct {
	Input  int `json:"input"`
	Output int `json:"output"`
}

// New creates a new V2 client.
func New(baseURL, password string, disableBuildAgent bool) *Client {
	return &Client{
		baseURL:           strings.TrimRight(baseURL, "/"),
		password:          password,
		httpClient:        &http.Client{Timeout: 5 * time.Minute},
		disableBuildAgent: disableBuildAgent,
	}
}

// CreateSession creates a new session on the V2 server.
func (c *Client) CreateSession(ctx context.Context) (string, error) {
	url := c.baseURL + "/api/session"
	
	body := map[string]any{
		"disableBuildAgent": c.disableBuildAgent,
	}
	
	req, err := http.NewRequestWithContext(ctx, "POST", url, jsonBody(body))
	if err != nil {
		return "", fmt.Errorf("create session request: %w", err)
	}
	
	c.applyAuth(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create session status %d: %s", resp.StatusCode, string(b))
	}
	
	var result struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("parse session response: %w", err)
	}
	
	if result.Data.ID == "" {
		return "", fmt.Errorf("empty session ID in response")
	}
	
	c.mu.Lock()
	c.sessionID = result.Data.ID
	c.sessionExpiry = time.Now().Add(30 * time.Minute)
	c.mu.Unlock()
	
	return result.Data.ID, nil
}

// SwitchModel switches the active model for a session.
func (c *Client) SwitchModel(ctx context.Context, sessionID, modelID string) error {
	url := c.baseURL + "/api/session/" + sessionID + "/model"
	
	body := map[string]any{
		"model": map[string]string{
			"id":         modelID,
			"providerID": "opencode",
		},
	}
	
	req, err := http.NewRequestWithContext(ctx, "POST", url, jsonBody(body))
	if err != nil {
		return fmt.Errorf("switch model request: %w", err)
	}
	
	c.applyAuth(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("switch model: %w", err)
	}
	defer resp.Body.Close()
	
	// V2 server returns 204 No Content for successful model switch
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("switch model status %d: %s", resp.StatusCode, string(b))
	}
	
	return nil
}

// SendPrompt sends a prompt to the V2 server.
func (c *Client) SendPrompt(ctx context.Context, sessionID, text string) error {
	url := c.baseURL + "/api/session/" + sessionID + "/prompt"
	
	body := map[string]any{
		"text": text,
	}
	
	// If disableBuildAgent is set, also explicitly disable at prompt level
	// as a safety measure (some V2 server versions may not respect session-level setting)
	if c.disableBuildAgent {
		body["buildAgent"] = false
		body["tools"] = []string{}
	}
	
	req, err := http.NewRequestWithContext(ctx, "POST", url, jsonBody(body))
	if err != nil {
		return fmt.Errorf("send prompt request: %w", err)
	}
	
	c.applyAuth(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send prompt: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("send prompt status %d: %s", resp.StatusCode, string(b))
	}
	
	return nil
}

// WaitSession waits for the V2 server to complete the current request.
func (c *Client) WaitSession(ctx context.Context, sessionID string, timeout time.Duration) error {
	url := c.baseURL + "/api/experimental/session/" + sessionID + "/wait"
	
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader("{}"))
	if err != nil {
		return fmt.Errorf("wait session request: %w", err)
	}
	
	c.applyAuth(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	
	resp, err := c.httpClient.Do(req.WithContext(timeoutCtx))
	if err != nil {
		return fmt.Errorf("wait session: %w", err)
	}
	defer resp.Body.Close()
	
	// Status 200 or 204 means wait completed
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("wait session status %d: %s", resp.StatusCode, string(b))
	}
	
	return nil
}

// GetMessages retrieves all messages from a session.
func (c *Client) GetMessages(ctx context.Context, sessionID string) ([]ChatMessage, error) {
	url := c.baseURL + "/api/session/" + sessionID + "/message"
	
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("get messages request: %w", err)
	}
	
	c.applyAuth(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get messages status %d: %s", resp.StatusCode, string(b))
	}
	
	var result struct {
		Data []ChatMessage `json:"data"`
	}
	
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse messages response: %w", err)
	}
	
	return result.Data, nil
}

// ListModels returns all available models from the V2 server.
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
	url := c.baseURL + "/api/model"
	
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("list models request: %w", err)
	}
	
	c.applyAuth(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list models status %d: %s", resp.StatusCode, string(b))
	}
	
	var result struct {
		Data []ModelInfo `json:"data"`
	}
	
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse models response: %w", err)
	}
	
	return result.Data, nil
}

// Chat sends a complete chat request through the V2 server, handling session
// creation, model switching, prompting, waiting, and message extraction.
// It returns a V2Response with text and tool calls (if any).
func (c *Client) Chat(ctx context.Context, modelID, text string) (*V2Response, error) {
	// Create a session with disableBuildAgent if configured
	sessionID, err := c.CreateSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	
	// Switch model
	if err := c.SwitchModel(ctx, sessionID, modelID); err != nil {
		return nil, fmt.Errorf("switch model: %w", err)
	}
	
	// Send prompt
	if err := c.SendPrompt(ctx, sessionID, text); err != nil {
		return nil, fmt.Errorf("send prompt: %w", err)
	}
	
	// Wait for completion
	if err := c.WaitSession(ctx, sessionID, 5*time.Minute); err != nil {
		return nil, fmt.Errorf("wait session: %w", err)
	}
	
	// Extract response from messages
	messages, err := c.GetMessages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}
	
	// Parse response - find the LAST assistant message (final state)
	var resp V2Response
	var lastAssistantMsg *ChatMessage
	for i := range messages {
		if messages[i].Type == "assistant" {
			lastAssistantMsg = &messages[i]
		}
	}
	
	if lastAssistantMsg == nil {
		return &V2Response{Finish: "stop"}, nil
	}
	
	// Extract text and tools from the last assistant message's content
	for _, block := range lastAssistantMsg.Content {
		if block.Type == "text" {
			resp.Text = block.Text
		} else if block.Type == "tool" {
			// This is a tool call block (e.g., from build agent)
			toolCall := ToolCallResult{
				ID:   block.ID,
				Type: "function",
				Function: ToolCallFunction{
					Name:      block.Name,
					Arguments: block.Args,
				},
			}
			resp.Tools = append(resp.Tools, toolCall)
		}
	}
	
	// Set finish reason from the message's finish field
	if lastAssistantMsg.Finish == "tool-calls" || lastAssistantMsg.Finish == "tool_calls" {
		resp.Finish = "tool_calls"
	} else if lastAssistantMsg.Fill != "" {
		resp.Finish = lastAssistantMsg.Fill
	} else if len(resp.Tools) > 0 {
		resp.Finish = "tool_calls"
	} else {
		resp.Finish = "stop"
	}
	
	// If finish is "stop" but we have tools, clear them (tools were already executed)
	if resp.Finish == "stop" && len(resp.Tools) > 0 {
		resp.Tools = nil
	}
	
	return &resp, nil
}

// applyAuth adds Basic auth header using the V2 server password.
func (c *Client) applyAuth(req *http.Request) {
	if c.password == "" {
		return
	}
	req.SetBasicAuth("opencode", c.password)
}

// jsonBody creates an io.Reader from a JSON-encoded value.
func jsonBody(v any) io.Reader {
	data, _ := json.Marshal(v)
	return bytes.NewReader(data)
}
