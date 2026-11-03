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
