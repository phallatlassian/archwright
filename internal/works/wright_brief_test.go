package works

import (
	"encoding/json"
	"strings"
	"testing"
)

// A read-only Wright runs in plan mode and read that as "present findings, do
// not act": it wrote its review as chat and never called report_result, so
// the step failed. Its brief says plan mode allows the call and that the call
// is how it ends.
func TestWrightBriefSaysPlanModeAllowsReportResult(t *testing.T) {
	b, err := parseBrief(json.RawMessage(goodBrief))
	if err != nil {
		t.Fatal(err)
	}
	got := b.Render("hallmark", "keep it small", 0, "")
	for _, want := range []string{"plan mode", "always allowed", "recorded as failed", "report_result"} {
		if !strings.Contains(got, want) {
			t.Fatalf("brief lacks %q:\n%s", want, got)
		}
	}
}
