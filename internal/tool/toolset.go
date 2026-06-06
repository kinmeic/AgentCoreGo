package tool

// Toolset defines a logical grouping of tools by domain.
// Toolsets can be enabled/disabled independently and composed via Includes.
type Toolset struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description" json:"description"`
	Tools       []string `yaml:"tools" json:"tools"`
	Includes    []string `yaml:"includes,omitempty" json:"includes,omitempty"`
	Enabled     bool     `yaml:"enabled" json:"enabled"`
}

// ResolvedTools returns the full set of tool names in this toolset,
// including tools from included toolsets. Requires the full toolset map
// to resolve includes recursively.
func (ts *Toolset) ResolvedTools(all map[string]*Toolset) []string {
	seen := make(map[string]bool)
	visited := make(map[string]bool)
	ts.resolve(all, seen, visited)
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	return result
}

func (ts *Toolset) resolve(all map[string]*Toolset, seen map[string]bool, visited map[string]bool) {
	if visited[ts.Name] {
		return // Prevent infinite recursion on cycles
	}
	visited[ts.Name] = true
	for _, t := range ts.Tools {
		seen[t] = true
	}
	for _, inc := range ts.Includes {
		if other, ok := all[inc]; ok {
			other.resolve(all, seen, visited)
		}
	}
}
