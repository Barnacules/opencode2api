package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"opencode2api/internal/identity"
	"opencode2api/internal/jsonutil"
	wire "opencode2api/internal/protocol"
	"opencode2api/internal/telemetry"
	"opencode2api/internal/v2"
)

// handleV2ToolResults handles tool results sent by the agentic client
// after executing tool calls from a V2 model. This enables the full
// multi-turn tool-calling loop: V2 returns tool_calls → client executes →
// client sends results here → proxy forwards to V2 → V2 returns final answer.
func (g *Gateway) handleV2ToolResults(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		wire.WriteError(w, wire.Chat, http.StatusBadRequest, "invalid request body", "invalid_request_error", "")
		return
	}

	var input map[string]any
	if err := json.Unmarshal(body, &input); err != nil {
		wire.WriteError(w, wire.Chat, http.StatusBadRequest, "request body must be JSON", "invalid_request_error", "")
		return
	}

	model := jsonutil.StringAt(input, "model")
	if model == "" {
		wire.WriteError(w, wire.Chat, http.StatusBadRequest, "model is required", "invalid_request_error", "model")
		return
	}

	stream := jsonutil.BoolAt(input, "stream")
	ids := identity.DeriveRequestIDs(r, input)
	meta := telemetry.MetaFromRequest(r)
	if meta != nil {
		meta.Model = model
		meta.Request = ids.Request
		meta.Tier = "v2"
		meta.Protocol = wire.Chat
	}

	// Build the continuation from messages (which may include tool results)
	var promptBuilder strings.Builder
	if messages, ok := input["messages"].([]any); ok {
		for _, rawMsg := range messages {
			if msg, ok := rawMsg.(map[string]any); ok {
				role := jsonutil.StringAt(msg, "role")
				content := jsonutil.StringAt(msg, "content")
				if content != "" {
					if role == "tool" {
						toolID := jsonutil.StringAt(msg, "tool_call_id")
						promptBuilder.WriteString(fmt.Sprintf("Tool result %s: %s\n", toolID, content))
					} else {
						promptBuilder.WriteString(fmt.Sprintf("%s: %s\n", strings.ToUpper(role), content))
					}
				}
			}
		}
	}

	prompt := strings.TrimSpace(promptBuilder.String())
	if prompt == "" {
		wire.WriteError(w, wire.Chat, http.StatusBadRequest, "no prompt content", "invalid_request_error", "")
		return
	}

	// Get or create V2 session
	sessionID, client, err := g.lookupOrCreateV2Session(ids.Request, model, r)
	if err != nil {
		g.logger.Error("v2 session", "error", err)
		wire.WriteError(w, wire.Chat, http.StatusBadGateway, "v2 session error", "v2_error", ids.Request)
		return
	}

	// Send prompt continuation to the V2 session
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(g.cfg.Retry.TimeoutSeconds)*time.Second)
	defer cancel()

	if err := client.SendPrompt(ctx, sessionID, prompt); err != nil {
		g.logger.Error("v2 send prompt", "error", err)
		wire.WriteError(w, wire.Chat, http.StatusBadGateway, "V2 server failed", "v2_error", ids.Request)
		return
	}

	if err := client.WaitSession(ctx, sessionID, 5*time.Minute); err != nil {
		g.logger.Error("v2 wait", "error", err)
		wire.WriteError(w, wire.Chat, http.StatusBadGateway, "V2 server failed", "v2_error", ids.Request)
		return
	}

	// Get the response
	msgs, err := client.GetMessages(ctx, sessionID)
	if err != nil {
		g.logger.Error("v2 get messages", "error", err)
		wire.WriteError(w, wire.Chat, http.StatusBadGateway, "V2 server failed", "v2_error", ids.Request)
		return
	}

	// Extract the answer (last stop message)
	resp := extractV2Answer(msgs)

	// Build response
	g.writeV2Response(w, stream, model, resp, ids.Request)

	if meta != nil {
		meta.Via = "v2"
		meta.Stream = stream
	}
}

// lookupOrCreateV2Session returns an existing V2 session or creates a new one.
func (g *Gateway) lookupOrCreateV2Session(requestID, model string, r *http.Request) (sessionID string, client *v2.Client, err error) {
	// Check for explicit V2 session header
	sessionID = r.Header.Get("X-V2-Session-ID")
	if sessionID == "" {
		// Try the stored session store
		if entry := g.v2Sessions.Get(requestID); entry != nil {
			return entry.SessionID, entry.Client, nil
		}
	}

	// Create a new V2 session
	if len(g.v2Clients) == 0 {
		return "", nil, fmt.Errorf("no V2 servers configured")
	}

	client = g.v2Clients[0]

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	sid, err := client.CreateSession(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("create session: %w", err)
	}

	if err := client.SwitchModel(ctx, sid, model); err != nil {
		return "", nil, fmt.Errorf("switch model: %w", err)
	}

	g.v2Sessions.Set(requestID, &v2SessionEntry{
		Client:    client,
		SessionID: sid,
		LastModel: model,
	})

	return sid, client, nil
}

// extractV2Answer extracts the final answer from V2 messages (last stop message).
func extractV2Answer(msgs []v2.ChatMessage) *v2.V2Response {
	var resp v2.V2Response
	var lastStopMsg *v2.ChatMessage
	var lastAnyMsg *v2.ChatMessage

	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Type == "assistant" {
			if msgs[i].Finish == "stop" && lastStopMsg == nil {
				lastStopMsg = &msgs[i]
			}
			if lastAnyMsg == nil {
				lastAnyMsg = &msgs[i]
			}
		}
	}

	chosen := lastStopMsg
	if chosen == nil {
		chosen = lastAnyMsg
	}

	if chosen == nil {
		return &resp
	}

	for _, block := range chosen.Content {
		if block.Type == "text" {
			resp.Text += block.Text
		}
	}

	if chosen.Finish == "stop" {
		resp.Finish = "stop"
	} else {
		resp.Finish = "tool_calls"
	}

	return &resp
}
