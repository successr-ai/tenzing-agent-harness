package compressor

import (
	"testing"

	"github.com/successr-ai/tenzing-agent-harness/pkg/common"
)

// Image blocks are charged a flat estimate — not their base64 length, which
// would treat one screenshot as ~megabytes of context.
func TestEstimateSizeCountsImagesAsConstant(t *testing.T) {
	c := NewCompressor(nil, 100_000)
	hugeBase64 := make([]byte, 1_000_000)
	msgs := []common.Message{{
		Role: common.RoleUser,
		Content: []common.ContentBlock{
			common.NewTextContent("look:"),
			common.NewImageContent("image/png", string(hugeBase64)),
		},
	}}
	got := c.EstimateSize(msgs)
	want := len("look:") + imageEstimateChars
	if got != want {
		t.Errorf("EstimateSize = %d, want %d (flat image estimate)", got, want)
	}
}
