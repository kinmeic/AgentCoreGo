package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-core/internal/model"

	"github.com/google/uuid"
)

// ApprovalRequest is a pending, user-visible authorization decision.
type ApprovalRequest struct {
	RequestID      string       `json:"request_id"`
	ConversationID string       `json:"conversation_id"`
	RunID          string       `json:"run_id"`
	ToolCallID     string       `json:"tool_call_id"`
	ToolName       string       `json:"tool"`
	Preview        string       `json:"preview"`
	Capabilities   Capabilities `json:"capabilities"`
	CreatedAt      time.Time    `json:"created_at"`
	ExpiresAt      time.Time    `json:"expires_at"`
}

// ApprovalResponse settles one approval request.
type ApprovalResponse struct {
	Decision string `json:"decision"`
	Scope    string `json:"scope,omitempty"`
}

type pendingApproval struct {
	request ApprovalRequest
	result  chan ApprovalResponse
	settled bool
}

// PermissionManager applies run modes and coordinates asynchronous approvals.
type PermissionManager struct {
	mode    string
	timeout time.Duration
	mu      sync.RWMutex
	pending map[string]*pendingApproval
	grants  map[string]bool
}

func NewPermissionManager(mode string) *PermissionManager {
	if mode == "" || mode == "smart" {
		mode = "build"
	}
	return &PermissionManager{mode: mode, timeout: 5 * time.Minute,
		pending: make(map[string]*pendingApproval), grants: make(map[string]bool)}
}

func (m *PermissionManager) Mode() string { return m.mode }

// Authorize returns nil only when the current mode or a user response allows execution.
func (m *PermissionManager) Authorize(ctx context.Context, registered *RegisteredTool, args map[string]any) error {
	if registered == nil {
		return errors.New("tool is not registered")
	}
	caps := registered.Capabilities
	decision := m.decision(caps)
	if decision == "allow" {
		return nil
	}
	if decision == "deny" {
		return fmt.Errorf("permission denied in %s mode for %s", m.mode, registered.Provider.Name())
	}
	info, ok := ExecutionInfoFromContext(ctx)
	if !ok || !info.Interactive || info.Emit == nil {
		return fmt.Errorf("approval required for %s; use the streaming API with an approval client", registered.Provider.Name())
	}
	grantKey := info.ConversationID + "\x00" + registered.Provider.Name()
	m.mu.RLock()
	granted := m.grants[grantKey]
	m.mu.RUnlock()
	if granted {
		return nil
	}

	now := time.Now()
	request := ApprovalRequest{
		RequestID: uuid.NewString(), ConversationID: info.ConversationID,
		RunID: info.RunID, ToolCallID: info.ToolCallID, ToolName: registered.Provider.Name(),
		Preview: PreviewArgs(args), Capabilities: caps, CreatedAt: now, ExpiresAt: now.Add(m.timeout),
	}
	pending := &pendingApproval{request: request, result: make(chan ApprovalResponse, 1)}
	m.mu.Lock()
	m.pending[request.RequestID] = pending
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.pending, request.RequestID)
		m.mu.Unlock()
	}()

	if info.OnStatus != nil {
		info.OnStatus(model.RunStatusWaitingApproval)
		defer info.OnStatus(model.RunStatusRunning)
	}
	if err := info.Emit(model.StreamEvent{Type: "approval.required", RequestID: request.RequestID,
		ToolCallID: request.ToolCallID, ToolName: request.ToolName, Preview: request.Preview}); err != nil {
		return fmt.Errorf("persist approval request: %w", err)
	}

	timer := time.NewTimer(m.timeout)
	defer timer.Stop()
	select {
	case response := <-pending.result:
		if response.Decision != "allow" {
			return fmt.Errorf("permission denied for %s", request.ToolName)
		}
		if response.Scope == "conversation" {
			m.mu.Lock()
			m.grants[grantKey] = true
			m.mu.Unlock()
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("permission request timed out for %s", request.ToolName)
	}
}

// PreviewArgs returns a bounded, recursively redacted argument preview.
func PreviewArgs(args map[string]any) string {
	encoded, _ := json.Marshal(redactPreviewValue(args))
	value := string(encoded)
	runes := []rune(value)
	if len(runes) > 500 {
		value = string(runes[:500]) + "..."
	}
	return value
}

func redactPreviewValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			lower := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
			sensitive := false
			for _, marker := range []string{"token", "secret", "password", "api_key", "apikey", "authorization", "credential"} {
				if strings.Contains(lower, marker) {
					sensitive = true
					break
				}
			}
			if sensitive {
				result[key] = "***"
			} else {
				result[key] = redactPreviewValue(item)
			}
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = redactPreviewValue(item)
		}
		return result
	default:
		return value
	}
}

func (m *PermissionManager) decision(caps Capabilities) string {
	switch m.mode {
	case "plan":
		if caps.ReadOnly {
			return "allow"
		}
		return "deny"
	case "edit":
		if caps.ReadOnly || (caps.SideEffectScope == "workspace" && !caps.Destructive) {
			return "allow"
		}
		return "ask"
	case "build":
		if caps.ReadOnly || (!caps.NeedsApproval && !caps.Destructive && caps.RiskLevel != "high") {
			return "allow"
		}
		return "ask"
	case "guarded":
		if caps.Destructive || caps.RiskLevel == "high" || caps.NeedsApproval {
			return "ask"
		}
		return "allow"
	case "yolo":
		return "allow"
	default:
		return "deny"
	}
}

// Respond resolves a request by request ID or tool-call ID.
func (m *PermissionManager) Respond(id string, response ApprovalResponse) bool {
	if response.Decision != "allow" && response.Decision != "deny" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var pending *pendingApproval
	for key, candidate := range m.pending {
		if key == id || candidate.request.ToolCallID == id {
			pending = candidate
			break
		}
	}
	if pending == nil || pending.settled {
		return false
	}
	select {
	case pending.result <- response:
		pending.settled = true
		return true
	default:
		return false
	}
}

// Pending returns a snapshot of all outstanding approvals.
func (m *PermissionManager) Pending() []ApprovalRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	requests := make([]ApprovalRequest, 0, len(m.pending))
	for _, pending := range m.pending {
		requests = append(requests, pending.request)
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].CreatedAt.Before(requests[j].CreatedAt) })
	return requests
}
