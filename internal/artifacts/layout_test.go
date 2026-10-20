package artifacts

import (
	"strings"
	"testing"
)

func TestPowerPointRejectsDenseLayoutBeforePublishing(t *testing.T) {
	var bullets []string
	for i := 0; i < 8; i++ {
		bullets = append(bullets, strings.Repeat("长", 100))
	}
	_, _, err := Create("pptx", Spec{Title: "报告", Slides: []Slide{{Title: "风险", Bullets: bullets}}})
	if err == nil || !strings.Contains(err.Error(), "溢出") {
		t.Fatalf("dense slide would silently overflow: %v", err)
	}
	_, _, err = Create("pptx", Spec{Title: "报告", Slides: []Slide{{Title: strings.Repeat("长标题", 20)}}})
	if err == nil {
		t.Fatal("oversized title was accepted")
	}
}
