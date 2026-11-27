// Every reason policy.Withheld gives has its own WithheldReason over the
// wire (the Inbox for people, PR 2a): none is sent as UNSPECIFIED, and no
// two share one.
package api

import (
	"testing"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/policy"
)

func TestWithheldReasons_EveryPolicyReason(t *testing.T) {
	want := map[string]kernelv1.WithheldReason{
		policy.ReasonOwnRequest:     kernelv1.WithheldReason_WITHHELD_REASON_OWN_REQUEST,
		policy.ReasonOwnContainer:   kernelv1.WithheldReason_WITHHELD_REASON_OWN_CONTAINER,
		policy.ReasonClaimedByOther: kernelv1.WithheldReason_WITHHELD_REASON_CLAIMED_BY_OTHER,
	}
	for reason, w := range want {
		if got, ok := withheldReasons[reason]; !ok || got != w {
			t.Errorf("withheldReasons[%q] = %v (present %v), want %v", reason, got, ok, w)
		}
	}
	if len(withheldReasons) != len(want) {
		t.Errorf("withheldReasons has %d entries, want %d: %v", len(withheldReasons), len(want), withheldReasons)
	}
}
