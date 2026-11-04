package agent

import "strings"

// ThinkSegment preserves the order of visible answer and embedded reasoning text.
type ThinkSegment struct {
	Reasoning bool
	Text      string
}

// StreamingThinkFilter separates complete and split <think> blocks without
// leaking partial tags into either output stream.
type StreamingThinkFilter struct {
	buffer  string
	inThink bool
}

func appendThinkSegment(segments []ThinkSegment, reasoning bool, text string) []ThinkSegment {
	if text == "" {
		return segments
	}
	if len(segments) != 0 && segments[len(segments)-1].Reasoning == reasoning {
		segments[len(segments)-1].Text += text
		return segments
	}
	return append(segments, ThinkSegment{Reasoning: reasoning, Text: text})
}

func (f *StreamingThinkFilter) ProcessSegments(chunk string) []ThinkSegment {
	f.buffer += chunk
	var segments []ThinkSegment
	for {
		if f.inThink {
			index := strings.Index(f.buffer, "</think>")
			if index < 0 {
				if len(f.buffer) > 7 {
					segments = appendThinkSegment(segments, true, f.buffer[:len(f.buffer)-7])
					f.buffer = f.buffer[len(f.buffer)-7:]
				}
				return segments
			}
			segments = appendThinkSegment(segments, true, f.buffer[:index])
			f.inThink = false
			f.buffer = f.buffer[index+8:]
			continue
		}
		index := strings.Index(f.buffer, "<think>")
		if index >= 0 {
			segments = appendThinkSegment(segments, false, f.buffer[:index])
			f.inThink = true
			f.buffer = f.buffer[index+7:]
			continue
		}
		safe := len(f.buffer) - 6
		if safe > 0 {
			segments = appendThinkSegment(segments, false, f.buffer[:safe])
			f.buffer = f.buffer[safe:]
		}
		return segments
	}
}

func (f *StreamingThinkFilter) FlushSegments() []ThinkSegment {
	segments := appendThinkSegment(nil, f.inThink, f.buffer)
	f.buffer = ""
	f.inThink = false
	return segments
}

func visibleThinkSegments(segments []ThinkSegment) string {
	var visible strings.Builder
	for _, segment := range segments {
		if !segment.Reasoning {
			visible.WriteString(segment.Text)
		}
	}
	return visible.String()
}

func (f *StreamingThinkFilter) Process(chunk string) string {
	return visibleThinkSegments(f.ProcessSegments(chunk))
}

func (f *StreamingThinkFilter) Flush() string {
	return visibleThinkSegments(f.FlushSegments())
}

// FilterThinkTags removes think blocks from a complete assistant response.
func FilterThinkTags(content string) string {
	var filter StreamingThinkFilter
	return strings.TrimSpace(filter.Process(content) + filter.Flush())
}
