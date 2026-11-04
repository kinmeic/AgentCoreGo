package tool

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"agent-core/internal/model"
	"agent-core/internal/security"
)

// Registry manages all available tools from different sources.
type Registry struct {
	mu              sync.RWMutex
	tools           map[string]*RegisteredTool
	policyCheck     func(ctx context.Context, toolName string, args map[string]any) error
	version         int64
	toolsets        map[string]*Toolset
	enabledToolsets map[string]bool
	slots           chan struct{}
}

// NewRegistry creates a new tool registry.
func NewRegistry() *Registry {
	return &Registry{
		slots:    make(chan struct{}, 10),
		tools:    make(map[string]*RegisteredTool),
		toolsets: DefaultToolsets(),
	}
}

// Register adds a tool provider and rejects ambiguous name collisions.
func (r *Registry) Register(provider Provider, source Source, origin string) error {
	if provider == nil || provider.Name() == "" {
		return fmt.Errorf("tool provider and name are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	name := provider.Name()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool %q is already registered", name)
	}
	caps := DefaultCapabilities(source)
	if declared, ok := provider.(CapabilityProvider); ok {
		caps = declared.Capabilities()
	}
	if caps.Timeout <= 0 {
		caps.Timeout = 2 * time.Minute
	}
	if caps.MaxOutputBytes <= 0 {
		caps.MaxOutputBytes = 100_000
	}
	r.tools[name] = &RegisteredTool{Provider: provider, Source: source, Origin: origin, Capabilities: caps}
	r.version++
	return nil
}

// Unregister removes a non-builtin tool from the public registry.
func (r *Registry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	registered, ok := r.tools[name]
	if !ok {
		return fmt.Errorf("tool not found: %s", name)
	}
	if registered.Source == SourceBuiltin {
		return fmt.Errorf("builtin tool %q cannot be unregistered", name)
	}
	delete(r.tools, name)
	r.version++
	return nil
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

// ConfigureToolsets replaces toolset definitions and their enabled set.
func (r *Registry) ConfigureToolsets(toolsets map[string]*Toolset, enabled map[string]bool) error {
	for name := range enabled {
		if _, ok := toolsets[name]; !ok {
			return fmt.Errorf("unknown toolset %q", name)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolsets = toolsets
	r.enabledToolsets = enabled
	r.version++
	return nil
}

func (r *Registry) isToolEnabledLocked(name string) bool {
	if len(r.enabledToolsets) == 0 || IsCoreTool(name) {
		return true
	}
	registered := r.tools[name]
	if registered != nil && ((registered.Source == SourceMCP && r.enabledToolsets["mcp"]) ||
		(registered.Source == SourceHostCallback && r.enabledToolsets["host_callback"])) {
		return true
	}
	for setName := range r.enabledToolsets {
		if set := r.toolsets[setName]; set != nil {
			for _, candidate := range set.ResolvedTools(r.toolsets) {
				if candidate == name {
					return true
				}
			}
		}
	}
	return false
}

// IsToolEnabled reports whether a tool is enabled by its toolset.
func (r *Registry) IsToolEnabled(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.isToolEnabledLocked(name)
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
	sort.Slice(result, func(i, j int) bool { return result[i].Provider.Name() < result[j].Provider.Name() })
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
	sort.Strings(names)
	return names
}

// ToolDefinitions returns LLM tool definitions for all registered tools.
func (r *Registry) ToolDefinitions() []model.ToolDefinition {
	return r.ToolDefinitionsFor(nil)
}

// ToolDefinitionsFor returns only selected names; nil exposes the full registry.
func (r *Registry) ToolDefinitionsFor(visible map[string]bool) []model.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		if r.isToolEnabledLocked(name) && (visible == nil || visible[name]) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	defs := make([]model.ToolDefinition, 0, len(names))
	for _, name := range names {
		rt := r.tools[name]
		defs = append(defs, model.ToolDefinition{Type: "function", Function: model.FuncDef{
			Name: name, Description: rt.Provider.Description(), Parameters: schemaForProvider(rt.Provider),
		}})
	}
	return defs
}

// ToolDefinitionsForRun hides undiscovered tools once the registry exceeds the threshold.
func (r *Registry) ToolDefinitionsForRun(state *DiscoveryState) []model.ToolDefinition {
	if state == nil || !state.Progressive {
		return r.ToolDefinitionsFor(nil)
	}
	r.mu.RLock()
	count := 0
	for name := range r.tools {
		if r.isToolEnabledLocked(name) {
			count++
		}
	}
	r.mu.RUnlock()
	if count <= state.Threshold {
		return r.ToolDefinitionsFor(nil)
	}
	visible := make(map[string]bool)
	r.mu.RLock()
	for name, registered := range r.tools {
		if registered.AlwaysLoaded {
			visible[name] = true
		}
	}
	r.mu.RUnlock()
	for _, name := range CoreToolNames() {
		visible[name] = true
	}
	for _, name := range state.Names() {
		visible[name] = true
	}
	return r.ToolDefinitionsFor(visible)
}

func schemaForProvider(provider Provider) map[string]any {
	if raw, ok := provider.(RawSchemaProvider); ok {
		return raw.JSONSchema()
	}
	schema := provider.Schema()
	result := map[string]any{"type": schema.Type, "properties": schema.Properties}
	if len(schema.Required) > 0 {
		result["required"] = schema.Required
	}
	return result
}

// Dispatch validates, authorizes and executes a tool with bounded time and output.
func (r *Registry) Dispatch(ctx context.Context, name string, args map[string]any) (string, error) {
	r.mu.RLock()
	rt, ok := r.tools[name]
	enabled := ok && r.isToolEnabledLocked(name)
	policyCheck := r.policyCheck
	slots := r.slots
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("tool not found: %s", name)
	}
	if !enabled {
		return "", fmt.Errorf("tool %s is disabled by its toolset", name)
	}
	if args == nil {
		args = map[string]any{}
	}
	validation, err := security.ValidateJSONSchema(schemaForProvider(rt.Provider), args)
	if err != nil {
		return "", fmt.Errorf("invalid schema for tool %s: %w", name, err)
	}
	if !validation.Valid() {
		message := "invalid arguments"
		for i, item := range validation.Errors() {
			if i >= 3 {
				break
			}
			message += "; " + item.String()
		}
		return "", fmt.Errorf("%s", message)
	}
	if policyCheck != nil {
		if err := policyCheck(ctx, name, args); err != nil {
			return "", fmt.Errorf("blocked by policy: %w", err)
		}
	}
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	execCtx := ctx
	cancel := func() {}
	if rt.Capabilities.Timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, rt.Capabilities.Timeout)
	}
	defer cancel()
	result, execErr := rt.Provider.Execute(execCtx, args)
	return limitOutput(result, rt.Capabilities.MaxOutputBytes), execErr
}

func limitOutput(value string, maximum int) string {
	if maximum <= 0 || len(value) <= maximum {
		return value
	}
	end := maximum
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + "\n... [truncated by tool output budget]"
}

// SetPolicyCheck sets an optional policy check function that runs before tool execution.
func (r *Registry) SetPolicyCheck(check func(context.Context, string, map[string]any) error) {
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

// SetConcurrencyLimit configures the shared execution queue before serving requests.
func (r *Registry) SetConcurrencyLimit(n int) {
	if n < 1 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.slots = make(chan struct{}, n)
}

func (r *Registry) SetOriginAlwaysLoaded(origin string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, registered := range r.tools {
		if registered.Source == SourceMCP && registered.Origin == origin {
			registered.AlwaysLoaded = true
		}
	}
}
