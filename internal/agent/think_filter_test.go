package agent

import (
	"strings"
	"testing"
)

func TestStreamingThinkFilterAcrossChunks(t *testing.T) {
	filter := &StreamingThinkFilter{}
	var output strings.Builder
	for _, chunk := range []string{"before<th", "ink>secret", "</th", "ink>after"} {
		output.WriteString(filter.Process(chunk))
	}
	output.WriteString(filter.Flush())
	if output.String() != "beforeafter" {
		t.Fatalf("unexpected filtered output %q", output.String())
	}
}

func TestFilterThinkTagsDropsUnterminatedBlock(t *testing.T) {
	if got := FilterThinkTags("visible<think>unfinished"); got != "visible" {
		t.Fatalf("got %q", got)
	}
}

func TestStreamingThinkFilterSegmentsPreserveOrder(t *testing.T) {
	filter := &StreamingThinkFilter{}
	var segments []ThinkSegment
	for _, chunk := range []string{"before<th", "ink>secret", "</th", "ink>after"} {
		segments = append(segments, filter.ProcessSegments(chunk)...)
	}
	segments = append(segments, filter.FlushSegments()...)
	var visible, reasoning strings.Builder
	var order []bool
	for _, segment := range segments {
		if len(order) == 0 || order[len(order)-1] != segment.Reasoning {
			order = append(order, segment.Reasoning)
		}
		if segment.Reasoning {
			reasoning.WriteString(segment.Text)
		} else {
			visible.WriteString(segment.Text)
		}
	}
	if visible.String() != "before" || reasoning.String() != "secret" || len(order) != 3 || order[0] || !order[1] || order[2] {
		t.Fatalf("unexpected ordered segments: %#v", segments)
	}
}

func TestStreamingThinkFilterFlushSegmentsReturnsUnterminatedReasoning(t *testing.T) {
	filter := &StreamingThinkFilter{}
	segments := filter.ProcessSegments("visible<think>unfinished")
	segments = append(segments, filter.FlushSegments()...)
	var reasoning string
	for _, segment := range segments {
		if segment.Reasoning {
			reasoning += segment.Text
		}
	}
	if reasoning != "unfinished" {
		t.Fatalf("unterminated reasoning was lost: %#v", segments)
	}
}
