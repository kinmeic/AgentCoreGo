package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
)

const (
	// SearchThreshold is the minimum number of non-core tools before tool search activates.
	SearchThreshold = 10
	// BM25 parameters
	bm25K1 = 1.5
	bm25B  = 0.75
	// Max search results
	maxSearchResults = 10
)

// ToolCatalog is a searchable index of tool metadata for the tool_search bridge.
type ToolCatalog struct {
	entries    []catalogEntry
	docLengths []float64
	avgDL      float64
	docFreq    map[string]int
	nDocs      int
}

type catalogEntry struct {
	name        string
	description string
	tokens      []string
}

// BuildCatalog creates a searchable catalog from registered tools.
// Core tools are excluded (they're always visible).
func BuildCatalog(registry *Registry) *ToolCatalog {
	tools := registry.List()
	var entries []catalogEntry

	for _, rt := range tools {
		name := rt.Provider.Name()
		if IsCoreTool(name) || !registry.IsToolEnabled(name) {
			continue
		}
		desc := rt.Provider.Description()
		schema := rt.Provider.Schema()

		// Build searchable text: split snake_case name + description + param names
		text := splitSnakeCase(name) + " " + desc
		for paramName := range schema.Properties {
			text += " " + splitSnakeCase(paramName)
		}

		tokens := tokenize(text)
		entries = append(entries, catalogEntry{
			name:        name,
			description: desc,
			tokens:      tokens,
		})
	}

	if len(entries) == 0 {
		return &ToolCatalog{}
	}

	// Calculate document statistics
	docLengths := make([]float64, len(entries))
	docFreq := make(map[string]int)
	var totalLen float64

	for i, e := range entries {
		docLengths[i] = float64(len(e.tokens))
		totalLen += docLengths[i]

		// Document frequency: count unique terms per document
		seen := make(map[string]bool)
		for _, t := range e.tokens {
			if !seen[t] {
				docFreq[t]++
				seen[t] = true
			}
		}
	}

	return &ToolCatalog{
		entries:    entries,
		docLengths: docLengths,
		avgDL:      totalLen / float64(len(entries)),
		docFreq:    docFreq,
		nDocs:      len(entries),
	}
}

// Search performs BM25 search on the catalog and returns matching tool names.
func (c *ToolCatalog) Search(query string, limit int) []SearchResult {
	if limit <= 0 {
		limit = maxSearchResults
	}
	if c.nDocs == 0 {
		return nil
	}

	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return nil
	}

	// Score all documents
	scores := make([]float64, c.nDocs)
	for qi, qt := range queryTokens {
		_ = qi
		df := float64(c.docFreq[qt])
		if df == 0 {
			continue
		}
		idf := math.Log((float64(c.nDocs)-df+0.5)/(df+0.5) + 1)

		for di, entry := range c.entries {
			tf := countTerm(entry.tokens, qt)
			if tf == 0 {
				continue
			}
			dl := c.docLengths[di]
			num := float64(tf) * (bm25K1 + 1)
			den := float64(tf) + bm25K1*(1-bm25B+bm25B*dl/c.avgDL)
			scores[di] += idf * num / den
		}
	}

	// Fallback: substring match if BM25 found nothing
	hasResults := false
	for _, s := range scores {
		if s > 0 {
			hasResults = true
			break
		}
	}
	if !hasResults {
		return c.substringSearch(query, limit)
	}

	// Sort by score descending
	type scored struct {
		idx   int
		score float64
	}
	scored_results := make([]scored, 0, c.nDocs)
	for i, s := range scores {
		if s > 0 {
			scored_results = append(scored_results, scored{i, s})
		}
	}
	sort.Slice(scored_results, func(i, j int) bool {
		return scored_results[i].score > scored_results[j].score
	})

	// Return top results
	results := make([]SearchResult, 0, limit)
	for i, sr := range scored_results {
		if i >= limit {
			break
		}
		e := c.entries[sr.idx]
		results = append(results, SearchResult{
			Name:        e.name,
			Description: e.description,
			Score:       sr.score,
		})
	}

	return results
}

// SearchResult represents a single search match.
type SearchResult struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Score       float64 `json:"score"`
}

// substringSearch is the fallback when BM25 returns no results.
func (c *ToolCatalog) substringSearch(query string, limit int) []SearchResult {
	lower := strings.ToLower(query)
	var results []SearchResult

	for _, e := range c.entries {
		if strings.Contains(strings.ToLower(e.name), lower) ||
			strings.Contains(strings.ToLower(e.description), lower) {
			results = append(results, SearchResult{
				Name:        e.name,
				Description: e.description,
				Score:       0.1,
			})
		}
		if len(results) >= limit {
			break
		}
	}

	return results
}

// --- Bridge Tools ---

// ToolSearchTool implements the tool_search bridge for progressive tool discovery.
type ToolSearchTool struct {
	registry      *Registry
	cachedCatalog *ToolCatalog
	cachedVersion int64
	mu            sync.Mutex
}

func NewToolSearchTool(registry *Registry) *ToolSearchTool {
	return &ToolSearchTool{registry: registry}
}

// getCatalog returns the cached catalog, rebuilding only when the registry changes.
func (t *ToolSearchTool) getCatalog() *ToolCatalog {
	currentVersion := t.registry.Version()

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.cachedCatalog != nil && t.cachedVersion == currentVersion {
		return t.cachedCatalog
	}

	t.cachedCatalog = BuildCatalog(t.registry)
	t.cachedVersion = currentVersion
	return t.cachedCatalog
}

func (t *ToolSearchTool) Name() string { return "tool_search" }
func (t *ToolSearchTool) Capabilities() Capabilities {
	return Capabilities{ReadOnly: true, ConcurrentSafe: true, SideEffectScope: "none", RiskLevel: "low", MaxOutputBytes: 20_000}
}
func (t *ToolSearchTool) Description() string {
	return "Search for available tools by keyword. Returns matching tool names and descriptions. Use when you need a tool that isn't in your current set."
}
func (t *ToolSearchTool) Schema() ToolSchema {
	return ToolSchema{
		Type: "object",
		Properties: map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Search query describing the tool you need (e.g., 'database query', 'send email')",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of results. Default 5.",
			},
		},
		Required: []string{"query"},
	}
}

func (t *ToolSearchTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return "", fmt.Errorf("query is required")
	}

	limit := 5
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	catalog := t.getCatalog()
	results := catalog.Search(query, limit)
	if info, ok := ExecutionInfoFromContext(ctx); ok && info.Discovery != nil {
		for _, result := range results {
			info.Discovery.Discover(result.Name)
		}
	}

	if len(results) == 0 {
		return "No matching tools found. Try a different search query.", nil
	}

	var sb strings.Builder
	sb.WriteString("Found tools:\n")
	for _, r := range results {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", r.Name, r.Description))
	}
	sb.WriteString("\nUse tool_describe to get detailed parameter info, then call the tool directly.")
	return sb.String(), nil
}

// ToolDescribeTool implements the tool_describe bridge for detailed tool info.
type ToolDescribeTool struct {
	registry *Registry
}

func NewToolDescribeTool(registry *Registry) *ToolDescribeTool {
	return &ToolDescribeTool{registry: registry}
}

func (t *ToolDescribeTool) Name() string { return "tool_describe" }
func (t *ToolDescribeTool) Capabilities() Capabilities {
	return Capabilities{ReadOnly: true, ConcurrentSafe: true, SideEffectScope: "none", RiskLevel: "low", MaxOutputBytes: 50_000}
}
func (t *ToolDescribeTool) Description() string {
	return "Get detailed information about a specific tool, including its parameters and usage instructions."
}
func (t *ToolDescribeTool) Schema() ToolSchema {
	return ToolSchema{
		Type: "object",
		Properties: map[string]any{
			"tool_name": map[string]any{
				"type":        "string",
				"description": "Name of the tool to describe",
			},
		},
		Required: []string{"tool_name"},
	}
}

func (t *ToolDescribeTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	name, ok := args["tool_name"].(string)
	if !ok || name == "" {
		return "", fmt.Errorf("tool_name is required")
	}

	rt, found := t.registry.Get(name)
	if !found {
		return fmt.Sprintf("Tool %q not found. Use tool_search to find available tools.", name), nil
	}

	schema := schemaForProvider(rt.Provider)
	schemaJSON, _ := json.MarshalIndent(schema, "", "  ")
	if info, ok := ExecutionInfoFromContext(ctx); ok && info.Discovery != nil {
		info.Discovery.Discover(name)
	}

	return fmt.Sprintf("Tool: %s\nDescription: %s\nSource: %s\nParameters:\n%s",
		rt.Provider.Name(), rt.Provider.Description(), rt.Source, string(schemaJSON)), nil
}

// --- Tokenization helpers ---

func tokenize(text string) []string {
	text = strings.ToLower(text)
	var tokens []string
	var current strings.Builder

	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current.WriteRune(r)
		} else {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}

	// Filter very short tokens (likely noise)
	filtered := tokens[:0]
	for _, t := range tokens {
		if len(t) >= 2 {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

func splitSnakeCase(s string) string {
	return strings.ReplaceAll(s, "_", " ")
}

func countTerm(tokens []string, term string) int {
	count := 0
	for _, t := range tokens {
		if t == term {
			count++
		}
	}
	return count
}
