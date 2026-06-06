package tool

import (
	"context"
	"fmt"
	"sync"

	"agent-core/internal/model"
)

// Registry manages all available tools from different sources.
type Registry struct {
	mu             sync.RWMutex
	tools          map[string]*RegisteredTool
	policyCheck    func(toolName string, args map[string]any) error // Optional policy check
	version        int64                                            // Incremented on Register/Unregister
}

// NewRegistry creates a new tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]*RegisteredTool),
	}
}

// Register adds a tool provider to the registry.
func (r *Registry) Register(provider Provider, source Source, origin string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[provider.Name()] = &RegisteredTool{
		Provider: provider,
		Source:   source,
		Origin:   origin,
	}
	r.version++
}

// Unregister removes a tool from the registry by name.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
	r.version++
}

// UnregisterByOrigin removes all tools from a specific origin (e.g., when an MCP server disconnects).
func (r *Registry) UnregisterByOrigin(source Source, origin string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := false
	for name, rt := range r.tools {
		if rt.Source == source && rt.Origin == origin {
			delete(r.tools, name)
			changed = true
		}
	}
	if changed {
		r.version++
	}
}

// Version returns the current registry version, incremented on every Register/Unregister.
func (r *Registry) Version() int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.version
}

// Get returns a registered tool by name.
func (r *Registry) Get(name string) (*RegisteredTool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.tools[name]
	return rt, ok
}

// List returns all registered tools.
func (r *Registry) List() []*RegisteredTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*RegisteredTool, 0, len(r.tools))
	for _, rt := range r.tools {
		result = append(result, rt)
	}
	return result
}

// Names returns the names of all registered tools.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// ToolDefinitions returns LLM tool definitions for all registered tools.
func (r *Registry) ToolDefinitions() []model.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	defs := make([]model.ToolDefinition, 0, len(r.tools))
	for _, rt := range r.tools {
		schema := rt.Provider.Schema()
		defs = append(defs, model.ToolDefinition{
			Type: "function",
			Function: model.FuncDef{
				Name:        rt.Provider.Name(),
				Description: rt.Provider.Description(),
				Parameters: map[string]any{
					"type":       schema.Type,
					"properties": schema.Properties,
					"required":   schema.Required,
				},
			},
		})
	}
	return defs
}

// Dispatch executes a tool by name with the given arguments.
func (r *Registry) Dispatch(ctx context.Context, name string, args map[string]any) (string, error) {
	r.mu.RLock()
	rt, ok := r.tools[name]
	policyCheck := r.policyCheck
	r.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("tool not found: %s", name)
	}

	// Apply policy check if configured
	if policyCheck != nil {
		if err := policyCheck(name, args); err != nil {
			return "", fmt.Errorf("blocked by policy: %w", err)
		}
	}

	return rt.Provider.Execute(ctx, args)
}

// SetPolicyCheck sets an optional policy check function that runs before tool execution.
func (r *Registry) SetPolicyCheck(check func(toolName string, args map[string]any) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policyCheck = check
}

// Count returns the number of registered tools.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}
