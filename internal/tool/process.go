package tool

import (
	"fmt"
	"log"
	"os/exec"
	"sync"
	"time"

	"github.com/google/uuid"
)

// TrackedProcess represents a subprocess spawned by a tool.
type TrackedProcess struct {
	ID        string
	Cmd       *exec.Cmd
	StartedAt time.Time
	Timeout   time.Duration
	ToolName  string
	ConvID    string
}

// ProcessRegistry tracks all subprocesses spawned by agent tools.
// It provides lifecycle management: timeout enforcement, bulk cleanup,
// and integration with conversation interruption.
type ProcessRegistry struct {
	mu        sync.RWMutex
	processes map[string]*TrackedProcess
}

// NewProcessRegistry creates a new process registry.
func NewProcessRegistry() *ProcessRegistry {
	return &ProcessRegistry{
		processes: make(map[string]*TrackedProcess),
	}
}

// Register adds a started process to the registry and returns its tracking ID.
// Call Start before Register so cmd.Process is fully published.
func (r *ProcessRegistry) Register(cmd *exec.Cmd, toolName, convID string, timeout time.Duration) string {
	id := uuid.New().String()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.processes[id] = &TrackedProcess{
		ID:        id,
		Cmd:       cmd,
		StartedAt: time.Now(),
		Timeout:   timeout,
		ToolName:  toolName,
		ConvID:    convID,
	}
	return id
}

// Unregister removes a process from the registry (called after it exits normally).
func (r *ProcessRegistry) Unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.processes, id)
}

// KillAll terminates all tracked processes. Called during shutdown.
func (r *ProcessRegistry) KillAll() {
	r.mu.Lock()
	procs := make([]*TrackedProcess, 0, len(r.processes))
	for _, p := range r.processes {
		procs = append(procs, p)
	}
	// Clear the map while holding the lock to prevent races with concurrent Register calls
	r.processes = make(map[string]*TrackedProcess)
	r.mu.Unlock()

	for _, p := range procs {
		r.killProcess(p)
	}
}

// KillByConversation terminates all processes for a specific conversation.
// Called when a conversation is interrupted/stopped.
func (r *ProcessRegistry) KillByConversation(convID string) int {
	r.mu.Lock()
	var toKill []string
	for id, p := range r.processes {
		if p.ConvID == convID {
			toKill = append(toKill, id)
		}
	}
	procs := make([]*TrackedProcess, 0, len(toKill))
	for _, id := range toKill {
		procs = append(procs, r.processes[id])
		delete(r.processes, id)
	}
	r.mu.Unlock()

	for _, p := range procs {
		r.killProcess(p)
	}
	return len(procs)
}

// Count returns the number of active tracked processes.
func (r *ProcessRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.processes)
}

// List returns info about all active processes.
func (r *ProcessRegistry) List() []ProcessInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]ProcessInfo, 0, len(r.processes))
	for _, p := range r.processes {
		result = append(result, ProcessInfo{
			ID:       p.ID,
			ToolName: p.ToolName,
			ConvID:   p.ConvID,
			Running:  time.Since(p.StartedAt),
			Timeout:  p.Timeout,
		})
	}
	return result
}

// ProcessInfo is a summary of a tracked process for API responses.
type ProcessInfo struct {
	ID       string        `json:"id"`
	ToolName string        `json:"tool_name"`
	ConvID   string        `json:"conversation_id"`
	Running  time.Duration `json:"running_duration"`
	Timeout  time.Duration `json:"timeout"`
}

func (r *ProcessRegistry) killProcess(p *TrackedProcess) {
	if p.Cmd == nil || p.Cmd.Process == nil {
		return
	}
	if err := killProcessTree(p.Cmd); err != nil {
		log.Printf("[process] Failed to kill process %s (%s): %v", p.ID, p.ToolName, err)
	} else {
		log.Printf("[process] Killed process %s (%s, running %v)", p.ID, p.ToolName, time.Since(p.StartedAt))
	}
}

// String returns a human-readable summary for debugging.
func (r *ProcessRegistry) String() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return fmt.Sprintf("ProcessRegistry(%d active)", len(r.processes))
}
