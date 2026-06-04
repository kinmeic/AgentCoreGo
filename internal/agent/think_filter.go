package agent

import (
	"regexp"
	"strings"
)

var thinkTagRegex = regexp.MustCompile(`(?s)<think>.*?</think>`)

// FilterThinkTags removes <think>...</think> blocks from assistant content.
func FilterThinkTags(content string) string {
	filtered := thinkTagRegex.ReplaceAllString(content, "")
	return strings.TrimSpace(filtered)
}
