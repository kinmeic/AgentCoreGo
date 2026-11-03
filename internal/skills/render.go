package skills

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"sort"
)

// RenderMetadata renders active (non-shadowed) skills as deterministic XML for
// insertion into an agent prompt. It returns an empty string when no active
// skills are available. Arbitrary frontmatter metadata is JSON encoded, which
// also gives its map keys a deterministic order.
func RenderMetadata(skills []Skill) string {
	ordered := append([]Skill(nil), skills...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Name == ordered[j].Name {
			return ordered[i].Path < ordered[j].Path
		}
		return ordered[i].Name < ordered[j].Name
	})

	var output bytes.Buffer
	for _, skill := range ordered {
		if skill.ShadowedBy != "" {
			continue
		}
		if output.Len() == 0 {
			output.WriteString("<available_skills>\n")
		}
		output.WriteString("  <skill>\n")
		writeElement(&output, "name", skill.Name)
		writeElement(&output, "description", skill.Description)
		if skill.WhenToUse != "" {
			writeElement(&output, "when_to_use", skill.WhenToUse)
		}
		if len(skill.Metadata) > 0 {
			if metadata, err := json.Marshal(skill.Metadata); err == nil {
				writeElement(&output, "metadata", string(metadata))
			}
		}
		writeElement(&output, "location", skill.Path)
		output.WriteString("  </skill>\n")
	}
	if output.Len() > 0 {
		output.WriteString("</available_skills>\n")
	}
	return output.String()
}

// RenderMetadata renders this catalog's active skill metadata.
func (c Catalog) RenderMetadata() string { return RenderMetadata(c.Skills) }

func writeElement(output *bytes.Buffer, name, value string) {
	output.WriteString("    <")
	output.WriteString(name)
	output.WriteString(">")
	_ = xml.EscapeText(output, []byte(value))
	output.WriteString("</")
	output.WriteString(name)
	output.WriteString(">\n")
}
