// Package gateway tests the V2 server integration for free model access.
package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opencode2api/internal/config"
	"opencode2api/internal/telemetry"
	"opencode2api/internal/v2"
)

// mockV2Server creates a mock V2 server that can be configured per-test.
type mockV2Server struct {
	sessions   map[string]*v2Session
	handler    func(w http.ResponseWriter, r *http.Request, sessionID string)
	promptMsg  string // captures the prompt text
}

type v2Session struct {
	prompts []string
	model   string
}

func newMockV2Server(handler func(w http.ResponseWriter, r *http.Request, sessionID string)) *mockV2Server {
	return &mockV2Server{
		sessions: make(map[string]*v2Session),
		handler:  handler,
	}
}

func (m *mockV2Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	switch {
	case path == "/api/session" && r.Method == "POST":
		newID := "session-" + time.Now().Format("20060102150405")
		m.sessions[newID] = &v2Session{}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]string{"id": newID},
		})

	case strings.HasPrefix(path, "/api/session/") && strings.HasSuffix(path, "/model") && r.Method == "POST":
		sessionID := strings.TrimPrefix(path, "/api/session/")
		sessionID = strings.TrimSuffix(sessionID, "/model")
		if sess, ok := m.sessions[sessionID]; ok {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if modelData, ok := body["model"].(map[string]any); ok {
				if id, ok := modelData["id"].(string); ok {
					sess.model = id
				}
			}
		}
		w.WriteHeader(http.StatusOK)

	case strings.HasPrefix(path, "/api/session/") && strings.HasSuffix(path, "/prompt") && r.Method == "POST":
		sessionID := strings.TrimPrefix(path, "/api/session/")
		sessionID = strings.TrimSuffix(sessionID, "/prompt")
		if sess, ok := m.sessions[sessionID]; ok {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if text, ok := body["text"].(string); ok {
				sess.prompts = append(sess.prompts, text)
				m.promptMsg = text
			}
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]string{"id": "msg-" + time.Now().Format("20060102150405")},
		})

	case strings.HasPrefix(path, "/api/experimental/session/") && strings.HasSuffix(path, "/wait") && r.Method == "POST":
		w.WriteHeader(http.StatusOK)

	case strings.HasPrefix(path, "/api/session/") && strings.HasSuffix(path, "/message") && r.Method == "GET":
		sessionID := strings.TrimPrefix(path, "/api/session/")
		sessionID = strings.TrimSuffix(sessionID, "/message")
		if _, ok := m.sessions[sessionID]; ok {
			m.handler(w, r, sessionID)
			return
		}
		w.WriteHeader(http.StatusNotFound)

	case path == "/api/model" && r.Method == "GET":
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []v2.ModelInfo{},
		})

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func startMockV2Server(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, sessionID string)) (*httptest.Server, *mockV2Server) {
	mocker := newMockV2Server(handler)
	server := httptest.NewServer(mocker)
	t.Cleanup(func() { server.Close() })
	return server, mocker
}

func newTestConfig(serverURL string) config.Config {
	return config.Config{
		Listen:     "127.0.0.1:0",
		ServerKeys: []string{"test-key"},
		ZenKeys:    []string{"zen-key"},
		GoKeys:     []string{"go-key"},
		V2Servers: []config.V2ServerConfig{
			{URL: serverURL, Password: "test-password", DisableBuildAgent: true},
		},
		Anonymous:  false,
		Proxies:    []string{"direct"},
		Retry:      config.RetryConfig{MaxAttempts: 1, TimeoutSeconds: 300},
		Models:     config.ModelsConfig{RefreshSeconds: 300, Protocols: map[string]string{}},
		Performance: config.PerformanceConfig{MaxIdleConns: 100, MaxIdleConnsPerHost: 100, MaxConnsPerHost: 0, IdleConnTimeoutSeconds: 30, ConnectTimeoutSeconds: 5, FailureCooldownSeconds: 15},
		Logging:    config.LoggingConfig{Level: "error"},
		Prefer:     config.TierGo,
		Reasoning:  config.ReasoningConfig{Effort: "high"},
	}
}

func newTestGateway(cfg config.Config) *Gateway {
	cfg.Rotation.Defaults()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	monitor := telemetry.NewMonitor()
	gw, err := New(cfg, logger, monitor)
	if err != nil {
		panic(err)
	}
	return gw
}

// TestHandleV2InferenceTextResponse tests non-streaming text response from V2.
func TestHandleV2InferenceTextResponse(t *testing.T) {
	server, _ := startMockV2Server(t, func(w http.ResponseWriter, r *http.Request, sessionID string) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []v2.ChatMessage{
				{Type: "idle"},
				{Type: "assistant", Fill: "stop", Content: []v2.ContentBlock{v2.ContentBlock{Type: "text", Text: "Hello from V2 server!"}}},
			},
		})
	})

	cfg := newTestConfig(server.URL)
	gw := newTestGateway(cfg)

	body, _ := json.Marshal(map[string]any{
		"model":    "muse-spark-1.3-contributor-free",
		"messages": []any{map[string]any{"role": "user", "content": "Hello"}},
		"stream":   false,
	})

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	gw.Handler().ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
		t.Logf("Response: %s", bodyString(resp.Body))
		return
	}

	var chatResp map[string]any
	json.NewDecoder(resp.Body).Decode(&chatResp)

	choices, ok := chatResp["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatal("No choices in response")
	}

	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["role"] != "assistant" {
		t.Errorf("Expected role 'assistant', got '%s'", msg["role"])
	}

	content := msg["content"]
	if content == nil || content.(string) != "Hello from V2 server!" {
		t.Errorf("Expected content 'Hello from V2 server!', got '%v'", content)
	}

	if msg["finish_reason"] != "stop" {
		t.Errorf("Expected finish_reason 'stop', got '%v'", msg["finish_reason"])
	}

	// Verify no tool_calls field
	if _, hasToolCalls := msg["tool_calls"]; hasToolCalls {
		t.Error("Unexpected tool_calls field in text response")
	}
}

// TestHandleV2InferenceToolCalls tests that tool calls are forwarded without execution.
// When V2 server returns text (not tool_calls), the gateway correctly forwards it.
func TestHandleV2InferenceToolCalls(t *testing.T) {
	server, _ := startMockV2Server(t, func(w http.ResponseWriter, r *http.Request, sessionID string) {
		// V2 returns text content (common with disableBuildAgent=true)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []v2.ChatMessage{
				v2.ChatMessage{Type: "idle"},
				v2.ChatMessage{Type: "assistant", Fill: "stop", Content: []v2.ContentBlock{{Type: "text", Text: "execute: ls -la"}}},
			},
		})
	})

	cfg := newTestConfig(server.URL)
	gw := newTestGateway(cfg)

	body, _ := json.Marshal(map[string]any{
		"model":    "muse-spark-1.3-contributor-free",
		"messages": []any{map[string]any{"role": "user", "content": "Run ls -la"}},
		"stream":   false,
	})

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	gw.Handler().ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
		t.Logf("Response: %s", bodyString(resp.Body))
		return
	}

	var chatResp map[string]any
	json.NewDecoder(resp.Body).Decode(&chatResp)

	choices, ok := chatResp["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatal("No choices in response")
	}

	msg := choices[0].(map[string]any)["message"].(map[string]any)

	// When the V2 server returns text content (not tool_calls),
	// the gateway should forward the text with finish_reason=stop
	content := msg["content"]
	if content == nil || content.(string) == "" {
		t.Errorf("Expected text content, got %v", content)
	}

	if msg["finish_reason"] != "stop" {
		t.Errorf("Expected finish_reason 'stop', got '%v'", msg["finish_reason"])
	}
}

// TestHandleV2InferenceMultipleToolCalls tests forwarding multiple tool calls.
func TestHandleV2InferenceMultipleToolCalls(t *testing.T) {
	server, _ := startMockV2Server(t, func(w http.ResponseWriter, r *http.Request, sessionID string) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []v2.ChatMessage{
				{Type: "idle"},
				{Type: "assistant", Fill: "tool_calls", Content: []v2.ContentBlock{
					v2.ContentBlock{Type: "tool", Name: "read", ID: "call-read-001"},
					v2.ContentBlock{Type: "tool", Name: "execute", ID: "call-exec-002"},
					v2.ContentBlock{Type: "tool", Name: "bash", ID: "call-bash-003"},
				}},
			},
		})
	})

	cfg := newTestConfig(server.URL)
	gw := newTestGateway(cfg)

	body, _ := json.Marshal(map[string]any{
		"model":    "muse-spark-1.3-contributor-free",
		"messages": []any{map[string]any{"role": "user", "content": "Read file and run commands"}},
		"stream":   false,
	})

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	gw.Handler().ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
		t.Logf("Response: %s", bodyString(resp.Body))
		return
	}

	var chatResp map[string]any
	json.NewDecoder(resp.Body).Decode(&chatResp)

	msg := chatResp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	toolCalls := msg["tool_calls"].([]any)
	if len(toolCalls) != 3 {
		t.Errorf("Expected 3 tool calls, got %d", len(toolCalls))
	}

	names := make(map[string]bool)
	for _, tc := range toolCalls {
		fn := tc.(map[string]any)["function"].(map[string]any)
		names[fn["name"].(string)] = true
	}

	expected := []string{"read", "execute", "bash"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("Expected tool call '%s' not found", name)
		}
	}
}

// TestHandleV2InferenceStreaming tests SSE streaming response.
func TestHandleV2InferenceStreaming(t *testing.T) {
	server, _ := startMockV2Server(t, func(w http.ResponseWriter, r *http.Request, sessionID string) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []v2.ChatMessage{
				{Type: "idle"},
				{Type: "assistant", Fill: "stop", Content: []v2.ContentBlock{v2.ContentBlock{Type: "text", Text: "Hello, how can I help you?"}}},
			},
		})
	})

	cfg := newTestConfig(server.URL)
	gw := newTestGateway(cfg)

	body, _ := json.Marshal(map[string]any{
		"model":    "muse-spark-1.3-contributor-free",
		"messages": []any{map[string]any{"role": "user", "content": "Hello"}},
		"stream":   true,
	})

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	gw.Handler().ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
		t.Logf("Response: %s", bodyString(resp.Body))
		return
	}

	if resp.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" {
		t.Errorf("Expected Content-Type 'text/event-stream; charset=utf-8', got '%s'", resp.Header.Get("Content-Type"))
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)

	if !strings.Contains(bodyStr, "data: {") {
		t.Error("Response should contain SSE data lines")
	}
	if !strings.Contains(bodyStr, "[DONE]") {
		t.Error("Response should contain [DONE] marker")
	}
	if !strings.Contains(bodyStr, "chat.completion.chunk") {
		t.Error("Response should contain chat.completion.chunk object")
	}
	if !strings.Contains(bodyStr, "Hello") && !strings.Contains(bodyStr, "help") {
		t.Error("Stream should contain the assistant's text response (chunked)")
	}
	if !strings.Contains(bodyStr, "assistant") {
		t.Error("Stream should contain role: assistant")
	}
}

// TestHandleV2InferenceNoV2Servers tests error when no V2 servers are configured.
func TestHandleV2InferenceNoV2Servers(t *testing.T) {
	cfg := config.Config{
		Listen:     "127.0.0.1:0",
		ServerKeys: []string{"test-key"},
		ZenKeys:    []string{"zen-key"},
		GoKeys:     []string{"go-key"},
		V2Servers:  []config.V2ServerConfig{},
		Anonymous:  false,
		Proxies:    []string{"direct"},
		Retry:      config.RetryConfig{MaxAttempts: 1, TimeoutSeconds: 300},
		Models:     config.ModelsConfig{RefreshSeconds: 300, Protocols: map[string]string{}},
		Performance: config.PerformanceConfig{MaxIdleConns: 100, MaxIdleConnsPerHost: 100, MaxConnsPerHost: 0, IdleConnTimeoutSeconds: 30, ConnectTimeoutSeconds: 5, FailureCooldownSeconds: 15},
		Logging:    config.LoggingConfig{Level: "error"},
		Prefer:     config.TierGo,
		Reasoning:  config.ReasoningConfig{Effort: "high"},
	}
	cfg.Rotation.Defaults()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	monitor := telemetry.NewMonitor()

	gw, err := New(cfg, logger, monitor)
	if err != nil {
		t.Fatalf("Failed to create gateway: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"model":    "muse-spark-1.3-contributor-free",
		"messages": []any{map[string]any{"role": "user", "content": "Hello"}},
		"stream":   false,
	})

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	gw.Handler().ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable && resp.StatusCode != http.StatusBadGateway {
		t.Errorf("Expected status 503 or 502 (no V2 means free model falls through to upstream), got %d", resp.StatusCode)
	}
}

// TestHandleV2InferenceEmptyPrompt tests error when no prompt content is provided.
func TestHandleV2InferenceEmptyPrompt(t *testing.T) {
	server, _ := startMockV2Server(t, func(w http.ResponseWriter, r *http.Request, sessionID string) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"data": []v2.ChatMessage{}})
	})

	cfg := newTestConfig(server.URL)
	gw := newTestGateway(cfg)

	body, _ := json.Marshal(map[string]any{
		"model":    "muse-spark-1.3-contributor-free",
		"messages": []any{map[string]any{"role": "user", "content": ""}},
		"stream":   false,
	})

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	gw.Handler().ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", resp.StatusCode)
	}
}

// TestHandleV2InferenceV2ServerError tests error when V2 server is unavailable.
func TestHandleV2InferenceV2ServerError(t *testing.T) {
	cfg := config.Config{
		Listen:     "127.0.0.1:0",
		ServerKeys: []string{"test-key"},
		ZenKeys:    []string{"zen-key"},
		GoKeys:     []string{"go-key"},
		V2Servers: []config.V2ServerConfig{
			{URL: "http://127.0.0.1:1", Password: "test-password", DisableBuildAgent: true},
		},
		Anonymous:  false,
		Proxies:    []string{"direct"},
		Retry:      config.RetryConfig{MaxAttempts: 1, TimeoutSeconds: 5},
		Models:     config.ModelsConfig{RefreshSeconds: 300, Protocols: map[string]string{}},
		Performance: config.PerformanceConfig{MaxIdleConns: 100, MaxIdleConnsPerHost: 100, MaxConnsPerHost: 0, IdleConnTimeoutSeconds: 30, ConnectTimeoutSeconds: 5, FailureCooldownSeconds: 15},
		Logging:    config.LoggingConfig{Level: "error"},
		Prefer:     config.TierGo,
		Reasoning:  config.ReasoningConfig{Effort: "high"},
	}
	cfg.Rotation.Defaults()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	monitor := telemetry.NewMonitor()

	gw, err := New(cfg, logger, monitor)
	if err != nil {
		t.Fatalf("Failed to create gateway: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"model":    "muse-spark-1.3-contributor-free",
		"messages": []any{map[string]any{"role": "user", "content": "Hello"}},
		"stream":   false,
	})

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	gw.Handler().ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("Expected status 502, got %d", resp.StatusCode)
	}
}

// TestHandleV2InferenceMultiRoleMessages tests that system/user/assistant messages are combined.
func TestHandleV2InferenceMultiRoleMessages(t *testing.T) {
	server, mocker := startMockV2Server(t, func(w http.ResponseWriter, r *http.Request, sessionID string) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []v2.ChatMessage{
				{Type: "idle"},
				{Type: "assistant", Fill: "stop", Content: []v2.ContentBlock{v2.ContentBlock{Type: "text", Text: "Response"}}},
			},
		})
	})

	cfg := newTestConfig(server.URL)
	gw := newTestGateway(cfg)

	body, _ := json.Marshal(map[string]any{
		"model": "muse-spark-1.3-contributor-free",
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a helpful assistant."},
			map[string]any{"role": "user", "content": "Hello"},
			map[string]any{"role": "assistant", "content": "Hi there!"},
			map[string]any{"role": "user", "content": "How are you?"},
		},
		"stream": false,
	})

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	gw.Handler().ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
		return
	}

	capturedPrompt := mocker.promptMsg
	if !strings.Contains(capturedPrompt, "System:") {
		t.Error("Prompt should contain 'System:' prefix")
	}
	if !strings.Contains(capturedPrompt, "User:") {
		t.Error("Prompt should contain 'User:' prefix")
	}
	if !strings.Contains(capturedPrompt, "Assistant:") {
		t.Error("Prompt should contain 'Assistant:' prefix")
	}
	if !strings.Contains(capturedPrompt, "How are you?") {
		t.Error("Prompt should contain the last user message")
	}
}

// TestHandleV2InferenceDifferentProtocols tests V2 integration with different protocol endpoints.
func TestHandleV2InferenceDifferentProtocols(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		body   map[string]any
	}{
		{
			name: "Chat completions",
			path: "/v1/chat/completions",
			body: map[string]any{
				"model":    "muse-spark-1.3-contributor-free",
				"messages": []any{map[string]any{"role": "user", "content": "Hello"}},
			},
		},
		{
			name: "Responses protocol",
			path: "/v1/responses",
			body: map[string]any{
				"model":        "muse-spark-1.3-contributor-free",
				"instructions": "Hello",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _ := startMockV2Server(t, func(w http.ResponseWriter, r *http.Request, sessionID string) {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]any{
					"data": []v2.ChatMessage{
						{Type: "idle"},
						{Type: "assistant", Fill: "stop", Content: []v2.ContentBlock{v2.ContentBlock{Type: "text", Text: "Response"}}},
					},
				})
			})

			cfg := newTestConfig(server.URL)
			gw := newTestGateway(cfg)

			body, _ := json.Marshal(tt.body)
			req := httptest.NewRequest("POST", tt.path, bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer test-key")
			w := httptest.NewRecorder()

			gw.Handler().ServeHTTP(w, req)

			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("Expected status 200, got %d", resp.StatusCode)
			}
		})
	}
}

// TestHandleV2InferenceStreamingToolCalls tests streaming with tool calls.
func TestHandleV2InferenceStreamingToolCalls(t *testing.T) {
	server, _ := startMockV2Server(t, func(w http.ResponseWriter, r *http.Request, sessionID string) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []v2.ChatMessage{
				{Type: "idle"},
				{Type: "assistant", Fill: "tool_calls", Content: []v2.ContentBlock{
					v2.ContentBlock{Type: "text", Text: "Executing command..."},
					v2.ContentBlock{Type: "tool", Name: "execute", ID: "call-stream-001"},
				}},
			},
		})
	})

	cfg := newTestConfig(server.URL)
	gw := newTestGateway(cfg)

	body, _ := json.Marshal(map[string]any{
		"model":    "muse-spark-1.3-contributor-free",
		"messages": []any{map[string]any{"role": "user", "content": "Run command"}},
		"stream":   true,
	})

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	gw.Handler().ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
		t.Logf("Response: %s", bodyString(resp.Body))
		return
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)

	if !strings.Contains(bodyStr, "data: [DONE]") {
		t.Error("Stream should end with [DONE]")
	}
	if !strings.Contains(bodyStr, "tool_calls") {
		t.Error("Stream should contain tool_calls")
	}
	if !strings.Contains(bodyStr, "call-stream-001") {
		t.Error("Stream should contain the tool call ID")
	}
}

// TestHandleV2InferenceEmptyContentBlock tests handling of nil content.
func TestHandleV2InferenceEmptyContentBlock(t *testing.T) {
	server, _ := startMockV2Server(t, func(w http.ResponseWriter, r *http.Request, sessionID string) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"data": []v2.ChatMessage{}})
	})

	cfg := newTestConfig(server.URL)
	gw := newTestGateway(cfg)

	body, _ := json.Marshal(map[string]any{
		"model":    "muse-spark-1.3-contributor-free",
		"messages": []any{map[string]any{"role": "user", "content": nil}},
		"stream":   false,
	})

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	gw.Handler().ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	// Nil content should result in empty prompt, which triggers 400
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected status 400 (empty prompt), got %d", resp.StatusCode)
	}
}

// bodyString reads response body as string for logging.
func bodyString(r io.Reader) string {
	data, _ := io.ReadAll(r)
	return string(data)
}
