// Tests for Step 5: the Type files in types/ that the bench and the
// versioning test rely on.
package typedef_test

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func parseFile(t *testing.T, name string) *typedef.Type {
	t.Helper()
	data, err := os.ReadFile("../../types/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	ty, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return ty
}

// ticket v2 adds `waiting`, with a timer back to in_progress, and shortens
// the in_progress timer. Fields and the rest of the lifecycle are unchanged.
func TestTicketV2_IsV1WithWaitingAndAShorterTimer(t *testing.T) {
	v1, v2 := parseFile(t, "ticket.v1.yaml"), parseFile(t, "ticket.v2.yaml")
	if v2.ID != "ticket" || v2.Version != 2 || v2.InitialState != v1.InitialState {
		t.Errorf("v2 = %s v%d starting in %q; want ticket v2 starting in %q",
			v2.ID, v2.Version, v2.InitialState, v1.InitialState)
	}
	if !reflect.DeepEqual(v2.Fields, v1.Fields) {
		t.Errorf("v2 fields = %v, want v1's %v", v2.Fields, v1.Fields)
	}

	s1, _ := v1.State("in_progress")
	s2, _ := v2.State("in_progress")
	if s2.Timer == nil || s2.Timer.To != "open" || s2.Timer.Duration >= s1.Timer.Duration {
		t.Errorf("v2 in_progress timer = %+v, want a timer to open shorter than v1's %v", s2.Timer, s1.Timer.Duration)
	}

	waiting, ok := v2.State("waiting")
	if !ok || waiting.Category != "waiting" {
		t.Fatalf("v2 waiting = %+v (found %v), want a state in category waiting", waiting, ok)
	}
	if waiting.Timer == nil || waiting.Timer.To != "in_progress" || waiting.Timer.Duration != 72*time.Hour {
		t.Errorf("v2 waiting timer = %+v, want 72h to in_progress", waiting.Timer)
	}
	for _, tr := range [][2]string{{"in_progress", "waiting"}, {"waiting", "in_progress"}} {
		if _, ok := v2.Transition(tr[0], tr[1]); !ok {
			t.Errorf("v2 has no transition %s -> %s", tr[0], tr[1])
		}
	}
	// Every v1 transition is still in v2.
	for _, tr := range v1.Transitions {
		if _, ok := v2.Transition(tr.From, tr.To); !ok {
			t.Errorf("v1 transition %s -> %s is missing from v2", tr.From, tr.To)
		}
	}
	if _, ok := v1.State("waiting"); ok {
		t.Error("v1 has a waiting state; it must stay as it was")
	}
}

// The burst Types have no lifecycle: each lives in the implicit state.
func TestChannelAndMessage_HaveNoLifecycle(t *testing.T) {
	for _, name := range []string{"channel.v1.yaml", "message.v1.yaml"} {
		ty := parseFile(t, name)
		if len(ty.States) != 1 || ty.States[0].ID != typedef.ImplicitState || len(ty.Transitions) != 0 {
			t.Errorf("%s: states = %v, transitions = %v; want only the implicit state", name, ty.States, ty.Transitions)
		}
	}
}
