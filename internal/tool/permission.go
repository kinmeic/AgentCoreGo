package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-core/internal/model"
	"agent-core/internal/security"

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
	alwaysAskTerminal   bool
	autoApproveHighRisk bool
	allowed             map[string]bool
	denied              map[string]bool
	mode                string
	timeout             time.Duration
	mu                  sync.RWMutex
	pending             map[string]*pendingApproval
	grants              map[string]bool
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
	caps := effectiveCapabilities(registered, args)
	info, hasInfo := ExecutionInfoFromContext(ctx)
	if hasInfo && info.ReadOnly && !caps.ReadOnly {
		return fmt.Errorf("permission denied for %s: run is read-only", registered.Provider.Name())
	}
	mode := m.mode
	if hasInfo && info.Mode != "" {
		mode = info.Mode
	}
	name := registered.Provider.Name()
	m.mu.RLock()
	denied, allowed := m.denied[name], m.allowed[name]
	alwaysAskTerminal := m.alwaysAskTerminal
	m.mu.RUnlock()
	if denied {
		return fmt.Errorf("permission.disallowedTools denies %s", name)
	}
	if mode == "guarded" && registered.Source == SourceBuiltin && name == "run_terminal" && !caps.ReadOnly {
		caps.AlwaysAsk = true
	}
	if alwaysAskTerminal && name == "run_terminal" && !caps.ReadOnly {
		switch mode {
		case "build", "edit", "guarded", "yolo":
			caps.AlwaysAsk = true
		}
	}
	decision := m.decisionForMode(mode, caps)
	if mode == "edit" && registered.Source == SourceBuiltin && caps.SideEffectScope == "workspace" {
		switch name {
		case "write_file", "edit_file", "delete_file", "move_file", "create_directory":
			decision = "allow"
		}
	}
	if allowed && mode != "auto" && mode != "plan" && !caps.AlwaysAsk {
		decision = "allow"
	}
	if caps.AlwaysAsk && mode != "auto" {
		decision = "ask"
	}
	if decision == "allow" {
		return nil
	}
	if decision == "deny" {
		return fmt.Errorf("permission denied in %s mode for %s", mode, registered.Provider.Name())
	}
	info, ok := ExecutionInfoFromContext(ctx)
	if !ok || !info.Interactive || info.Emit == nil {
		return fmt.Errorf("approval required for %s; use the streaming API with an approval client", registered.Provider.Name())
	}
	grantKey := info.ConversationID + "\x00" + registered.Provider.Name()
	m.mu.RLock()
	granted := m.grants[grantKey] && !caps.AlwaysAsk
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
		if response.Scope == "conversation" && !caps.AlwaysAsk {
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

func effectiveCapabilities(registered *RegisteredTool, args map[string]any) Capabilities {
	caps := registered.Capabilities
	if registered.Source == SourceBuiltin && registered.Provider.Name() == "run_terminal" && runtime.GOOS != "windows" {
		if command, ok := args["command"].(string); ok && security.IsReadOnlyShellCommand(command) {
			caps.ReadOnly = true
			caps.Destructive = false
			caps.NeedsApproval = false
			caps.SideEffectScope = "workspace"
			caps.RiskLevel = "low"
		}
	}
	return caps
}

func (m *PermissionManager) decisionForMode(mode string, caps Capabilities) string {
	switch mode {
	case "plan":
		if caps.ReadOnly {
			if !caps.NeedsApproval {
				return "allow"
			}
			return "ask"
		}
		if caps.SideEffectScope == "external" {
			return m.decisionForMode("build", caps)
		}
		return "deny"
	case "edit":
		return m.decisionForMode("build", caps)
	case "build":
		if caps.ReadOnly && !caps.Destructive && !caps.NeedsApproval {
			return "allow"
		}
		if caps.RiskLevel == "critical" || (caps.RiskLevel == "high" && !m.autoApproveHighRisk) {
			return "ask"
		}
		if caps.SideEffectScope == "session" && caps.RiskLevel == "low" && !caps.Destructive && !caps.NeedsApproval {
			return "allow"
		}
		if caps.NeedsApproval || caps.Destructive || (caps.SideEffectScope != "none" && caps.SideEffectScope != "") {
			return "ask"
		}
		return "allow"
	case "guarded":
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

// ConfigureRules applies explicit tool names before serving requests. Explicit deny
// remains authoritative even in yolo/guarded as a server security boundary.
func (m *PermissionManager) ConfigureRules(allowed, denied []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.allowed = map[string]bool{}
	m.denied = map[string]bool{}
	for _, name := range allowed {
		m.allowed[name] = true
	}
	for _, name := range denied {
		m.denied[name] = true
	}
}

func (m *PermissionManager) SetAutoApproveHighRisk(value bool) { m.autoApproveHighRisk = value }

// SetAlwaysAskTerminal requires per-call approval for every non-read-only terminal
// command in modes that can execute it. Deny decisions remain authoritative.
func (m *PermissionManager) SetAlwaysAskTerminal(value bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alwaysAskTerminal = value
}
