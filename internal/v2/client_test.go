// Package v2 tests the OpenCode V2 server client.
package v2

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestV2ClientBasicChat tests basic chat functionality with the V2 server.
func TestV2ClientBasicChat(t *testing.T) {
	// Mock V2 server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{
					"id": "test-session-123",
				},
			})
		case "/api/session/test-session-123/model":
			w.WriteHeader(http.StatusOK)
		case "/api/session/test-session-123/prompt":
			body, _ := io.ReadAll(r.Body)
			var bodyMap map[string]any
			json.Unmarshal(body, &bodyMap)
			
			// Check disableBuildAgent is set
			if bodyMap["buildAgent"] != false {
				t.Error("buildAgent should be false")
			}
			if bodyMap["tools"] == nil {
				t.Error("tools should be set")
			}
			
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{
					"id": "msg-123",
				},
			})
		case "/api/experimental/session/test-session-123/wait":
			w.WriteHeader(http.StatusOK)
		case "/api/session/test-session-123/message":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []ChatMessage{
					{
						Type:   "idle",
						Content: nil,
					},
					{
						Type:   "assistant",
						Fill:   "stop",
						Content: []ContentBlock{
							{Type: "text", Text: "Hello, how can I help you?"},
						},
					},
					{
						Type: "user",
					},
				},
			})
		case "/api/model":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []ModelInfo{
					{ModelID: "test-model", ProviderID: "opencode", Name: "Test Model", Enabled: true, Status: "active"},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := New(server.URL, "test-password", true)
	
	// Test Chat method
	resp, err := client.Chat(context.Background(), "muse-spark-1.3-contributor-free", "Hello")
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	
	if resp.Text != "Hello, how can I help you?" {
		t.Errorf("Expected text 'Hello, how can I help you?', got '%s'", resp.Text)
	}
	
	if resp.Finish != "stop" {
		t.Errorf("Expected finish 'stop', got '%s'", resp.Finish)
	}
}

// TestV2ClientToolCalls tests that tool calls are properly extracted and forwarded.
func TestV2ClientToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{
					"id": "test-session-456",
				},
			})
		case "/api/session/test-session-456/model":
			w.WriteHeader(http.StatusOK)
		case "/api/session/test-session-456/prompt":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{
					"id": "msg-456",
				},
			})
		case "/api/experimental/session/test-session-456/wait":
			w.WriteHeader(http.StatusOK)
		case "/api/session/test-session-456/message":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []ChatMessage{
					{Type: "idle"},
					{
						Type: "assistant",
						Fill: "tool_calls",
						Content: []ContentBlock{
							{Type: "text", Text: "Let me search for that..."},
							{Type: "tool", Name: "execute", ID: "call_123"},
						},
					},
				},
			})
		case "/api/model":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []ModelInfo{},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := New(server.URL, "test-password", true)
	
	resp, err := client.Chat(context.Background(), "muse-spark-1.3-contributor-free", "Search the filesystem")
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	
	// Verify tool calls are extracted
	if len(resp.Tools) == 0 {
		t.Error("Expected tool calls to be extracted")
	}
	
	if len(resp.Tools) > 0 {
		tool := resp.Tools[0]
		if tool.ID == "" {
			t.Error("Tool ID should not be empty")
		}
		if tool.Function.Name == "" {
			t.Error("Tool function name should not be empty")
		}
	}
	
	if resp.Finish != "tool_calls" {
		t.Errorf("Expected finish 'tool_calls', got '%s'", resp.Finish)
	}
}

// TestV2ClientSessionManagement tests session creation and reuse.
func TestV2ClientSessionManagement(t *testing.T) {
	var createdSessionID string
	var switchCalls int
	var promptCalls int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{
					"id": "new-session-789",
				},
			})
			createdSessionID = "new-session-789"
		case "/api/session/new-session-789/model":
			switchCalls++
			w.WriteHeader(http.StatusOK)
		case "/api/session/new-session-789/prompt":
			promptCalls++
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{
					"id": "msg-789",
				},
			})
		case "/api/experimental/session/new-session-789/wait":
			w.WriteHeader(http.StatusOK)
		case "/api/session/new-session-789/message":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []ChatMessage{
					{Type: "idle"},
					{Type: "assistant", Fill: "stop", Content: []ContentBlock{{Type: "text", Text: "Response"}}},
				},
			})
		case "/api/model":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []ModelInfo{},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := New(server.URL, "test-password", false)
	
	// First chat
	resp1, err := client.Chat(context.Background(), "muse-spark-1.3-contributor-free", "Hello 1")
	if err != nil {
		t.Fatalf("First chat failed: %v", err)
	}
	if createdSessionID != "new-session-789" {
		t.Errorf("Session ID mismatch: got '%s', want 'new-session-789'", createdSessionID)
	}
	if switchCalls != 1 {
		t.Errorf("Expected 1 switch call, got %d", switchCalls)
	}
	if promptCalls != 1 {
		t.Errorf("Expected 1 prompt call, got %d", promptCalls)
	}
	
	if resp1.Text != "Response" {
		t.Errorf("Expected text 'Response', got '%s'", resp1.Text)
	}
	
	// Second chat (creates new session)
	resp2, err := client.Chat(context.Background(), "muse-spark-1.3-contributor-free", "Hello 2")
	if err != nil {
		t.Fatalf("Second chat failed: %v", err)
	}
	
	if switchCalls != 2 {
		t.Errorf("Expected 2 switch calls, got %d", switchCalls)
	}
	if promptCalls != 2 {
		t.Errorf("Expected 2 prompt calls, got %d", promptCalls)
	}
	
	_ = resp2
}

// TestV2ClientDisableBuildAgent tests that disableBuildAgent is properly set.
func TestV2ClientDisableBuildAgent(t *testing.T) {
	var bodyReceived map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session":
			// Check disableBuildAgent in request body
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &bodyReceived)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{
					"id": "session-without-tools",
				},
			})
		case "/api/session/session-without-tools/model":
			w.WriteHeader(http.StatusOK)
		case "/api/session/session-without-tools/prompt":
			promptBody, _ := io.ReadAll(r.Body)
			json.Unmarshal(promptBody, &bodyReceived)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{
					"id": "msg-001",
				},
			})
		case "/api/experimental/session/session-without-tools/wait":
			w.WriteHeader(http.StatusOK)
		case "/api/session/session-without-tools/message":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []ChatMessage{
					{Type: "idle"},
					{Type: "assistant", Fill: "stop", Content: []ContentBlock{{Type: "text", Text: "Hello"}}},
				},
			})
		case "/api/model":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []ModelInfo{},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	// Test with disableBuildAgent = true
	client := New(server.URL, "test-password", true)
	
	_, err := client.Chat(context.Background(), "muse-spark-1.3-contributor-free", "Test")
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	
	// Check that disableBuildAgent was set in session creation
	if bodyReceived["disableBuildAgent"] != true {
		t.Error("disableBuildAgent should be true in session creation")
	}
}

// TestV2ClientAuth tests that basic auth is properly applied.
func TestV2ClientAuth(t *testing.T) {
	var receivedUser, receivedPass string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUser, receivedPass, _ = r.BasicAuth()
		switch r.URL.Path {
		case "/api/session":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{
					"id": "session-auth",
				},
			})
		case "/api/session/session-auth/model":
			w.WriteHeader(http.StatusOK)
		case "/api/session/session-auth/prompt":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{
					"id": "msg-auth",
				},
			})
		case "/api/experimental/session/session-auth/wait":
			w.WriteHeader(http.StatusOK)
		case "/api/session/session-auth/message":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []ChatMessage{
					{Type: "idle"},
					{Type: "assistant", Fill: "stop", Content: []ContentBlock{{Type: "text", Text: "Hello"}}},
				},
			})
		case "/api/model":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []ModelInfo{},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := New(server.URL, "test-password-abc", false)
	
	_, err := client.Chat(context.Background(), "muse-spark-1.3-contributor-free", "Test")
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	
	// The V2 server uses "opencode" as username and password as password
	if receivedUser != "opencode" {
		t.Errorf("Expected username 'opencode', got '%s'", receivedUser)
	}
	if receivedPass != "test-password-abc" {
		t.Errorf("Expected password 'test-password-abc', got '%s'", receivedPass)
	}
}

// TestV2ClientMultipleToolCalls tests extraction of multiple tool calls.
func TestV2ClientMultipleToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{
					"id": "session-multi",
				},
			})
		case "/api/session/session-multi/model":
			w.WriteHeader(http.StatusOK)
		case "/api/session/session-multi/prompt":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{
					"id": "msg-multi",
				},
			})
		case "/api/experimental/session/session-multi/wait":
			w.WriteHeader(http.StatusOK)
		case "/api/session/session-multi/message":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []ChatMessage{
					{Type: "idle"},
					{
						Type:   "assistant",
						Fill:   "tool_calls",
						Content: []ContentBlock{
							{Type: "text", Text: "Running multiple operations..."},
							{Type: "tool", Name: "execute", ID: "call_001"},
							{Type: "tool", Name: "bash", ID: "call_002"},
							{Type: "tool", Name: "read", ID: "call_003"},
						},
					},
				},
			})
		case "/api/model":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []ModelInfo{},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := New(server.URL, "test-password", true)
	
	resp, err := client.Chat(context.Background(), "muse-spark-1.3-contributor-free", "Run multiple commands")
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	
	if len(resp.Tools) != 3 {
		t.Errorf("Expected 3 tool calls, got %d", len(resp.Tools))
	}
	
	toolNames := make(map[string]bool)
	for _, tool := range resp.Tools {
		toolNames[tool.Function.Name] = true
	}
	
	expectedTools := []string{"execute", "bash", "read"}
	for _, name := range expectedTools {
		if !toolNames[name] {
			t.Errorf("Expected tool '%s' not found", name)
		}
	}
}

// TestV2ClientStreamingResponse tests SSE stream generation for chat responses.
func TestV2ClientStreamingResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{
					"id": "session-stream",
				},
			})
		case "/api/session/session-stream/model":
			w.WriteHeader(http.StatusOK)
		case "/api/session/session-stream/prompt":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{
					"id": "msg-stream",
				},
			})
		case "/api/experimental/session/session-stream/wait":
			w.WriteHeader(http.StatusOK)
		case "/api/session/session-stream/message":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []ChatMessage{
					{Type: "idle"},
					{Type: "assistant", Fill: "stop", Content: []ContentBlock{{Type: "text", Text: "Testing 123"}}},
				},
			})
		case "/api/model":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []ModelInfo{},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := New(server.URL, "test-password", true)
	
	resp, err := client.Chat(context.Background(), "muse-spark-1.3-contributor-free", "Test streaming")
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	
	// Verify the response structure matches expectations
	if resp.Text == "" {
		t.Error("Expected text content")
	}
	if resp.Finish != "stop" {
		t.Errorf("Expected finish 'stop', got '%s'", resp.Finish)
	}
}

// TestV2ClientErrorHandling tests error scenarios.
func TestV2ClientErrorHandling(t *testing.T) {
	tests := []struct {
		name        string
		handler     http.HandlerFunc
		expectError bool
	}{
		{
			name: "session creation fails",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/session" {
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte("server error"))
				}
			},
			expectError: true,
		},
		{
			name: "prompt fails",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/session" {
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"id": "session-err"}})
				} else if strings.HasSuffix(r.URL.Path, "/prompt") {
					w.WriteHeader(http.StatusTeapot)
					w.Write([]byte("teapot"))
				}
			},
			expectError: true,
		},
		{
			name: "wait fails",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/session" {
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"id": "session-wait"}})
				} else if strings.HasSuffix(r.URL.Path, "/model") {
					w.WriteHeader(http.StatusOK)
				} else if strings.HasSuffix(r.URL.Path, "/prompt") {
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"id": "msg-wait"}})
				} else if strings.HasSuffix(r.URL.Path, "/wait") {
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte("wait error"))
				}
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			client := New(server.URL, "test-password", false)
			_, err := client.Chat(context.Background(), "test-model", "Test")
			
			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}
