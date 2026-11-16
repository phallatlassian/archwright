package works

import (
	"reflect"
	"strings"
	"testing"

	"github.com/phallatlassian/archwright/internal/defs"
	"github.com/phallatlassian/archwright/internal/spool"
	"github.com/phallatlassian/archwright/internal/store"
)

func TestSetPlanRejectsUnknownGuildAndKeepsStoredPlan(t *testing.T) {
	g := newRig(t, nil)
	reg := defs.New(defs.Shipped(), "")
	reg.Reload()
	g.r.UseDefinitions(reg)
	job := g.job.ID

	var kept store.Plan
	g.ok(g.arch, "set_plan", `{"job_id":"`+job+`","steps":[{"id":"a","guild":"masonry","title":"build"}]}`, &kept)

	resp := g.call(g.arch, "set_plan", `{"job_id":"`+job+`","steps":[{"id":"a","guild":"masonry","title":"build"},{"id":"look","guild":"research","title":"look around"}]}`)
	if resp.OK || resp.Error == nil || resp.Error.Code != spool.CodeInvalidInput {
		t.Fatalf("want invalid input, got %+v", resp)
	}
	msg := resp.Error.Message
	if !strings.Contains(msg, `"look"`) || !strings.Contains(msg, `"research"`) {
		t.Errorf("error should name the step and guild: %s", msg)
	}
	for _, gd := range reg.Guilds() {
		if !strings.Contains(msg, gd.Name) {
			t.Errorf("error should list guild %q: %s", gd.Name, msg)
		}
	}
	after, err := g.st.GetPlan(job)
	if err != nil || !reflect.DeepEqual(after, kept) {
		t.Fatalf("stored Plan changed: %+v %v", after, err)
	}

	var ok store.Plan
	g.ok(g.arch, "set_plan", `{"job_id":"`+job+`","steps":[{"id":"a","guild":"survey","title":"look"},{"id":"b","guild":"hallmark","title":"review","blocked_by":["a"]}]}`, &ok)
	if len(ok.Steps) != 2 || ok.Approval != store.ApprovalPending {
		t.Fatalf("%+v", ok)
	}
}
