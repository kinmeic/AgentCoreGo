package agent

import "strings"

// StreamingThinkFilter removes complete and split <think> blocks without
// leaking partial tags or unterminated reasoning into assistant output.
type StreamingThinkFilter struct {
	buffer  string
	inThink bool
}

func (f *StreamingThinkFilter) Process(chunk string) string {
	f.buffer += chunk
	var visible strings.Builder
	for {
		if f.inThink {
			index := strings.Index(f.buffer, "</think>")
			if index < 0 {
				if len(f.buffer) > 7 {
					f.buffer = f.buffer[len(f.buffer)-7:]
				}
				return visible.String()
			}
			f.inThink = false
			f.buffer = f.buffer[index+8:]
			continue
		}
		index := strings.Index(f.buffer, "<think>")
		if index >= 0 {
			visible.WriteString(f.buffer[:index])
			f.inThink = true
			f.buffer = f.buffer[index+7:]
			continue
		}
		safe := len(f.buffer) - 6
		if safe > 0 {
			visible.WriteString(f.buffer[:safe])
			f.buffer = f.buffer[safe:]
		}
		return visible.String()
	}
}

func (f *StreamingThinkFilter) Flush() string {
	if f.inThink {
		f.buffer = ""
		return ""
	}
	value := f.buffer
	f.buffer = ""
	return value
}

// FilterThinkTags removes think blocks from a complete assistant response.
func FilterThinkTags(content string) string {
	var filter StreamingThinkFilter
	return strings.TrimSpace(filter.Process(content) + filter.Flush())
}
