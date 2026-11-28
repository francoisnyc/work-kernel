// types/ clean or explained (pli check, PR 2; the owner's decisions, Phase
// A of PR 2): every finding pli check gives on the Types in types/ is
// listed here with why it is there, and nothing else is found. A new
// finding fails the test until it is explained or fixed; so does an
// explained finding that goes away, so the list stays true.
package check_test

import (
	"fmt"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/check"
)

// Why each group of findings is there.
const (
	whyIncidentCancel = "real hole, fixed in incident@8 (the follow-up step after PR 2): in_progress -> canceled is open to anyone, an agent or the requester, while cancelling a new incident is the desk's or the requester's"
	whyIncidentNew    = "intended: a desk agent cancels a new incident, and an agent holding desk_agent may too"
	whyIncidentActive = "intended (incident@8, the follow-up step after PR 2): only a desk agent cancels an incident in progress, whether an agent holding the role or a desk agent who is its requester"
	whyDraft          = "real hole, closed by access_request@3 / vacation_request@5 (the follow-up step after PR 2): a colleague who sees the draft may change what is asked before the requester submits it; requester_only closes it"
	whyBuildStep      = "intended: whoever f_owner names (set by a maintainer) moves the step on or drops it, and may be an agent or the requester"
	whyEquipment      = "intended: the desk agent who took the request fulfils or declines it, a person or an agent holding desk_agent"
	whyFulfilment     = "intended: a desk agent completes the task; one who also asked for the access may complete their own (the Type does not forbid it)"
	whyLeaveBalance   = "intended: holders of hr_admin close a balance, their own included"
)

type explained struct {
	id      string
	version int
	key     key
	why     string
}

var typesFindings = []explained{
	{"build_step", 1, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[10]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 1, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[11]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 1, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[12]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 1, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[13]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 1, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[14]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 1, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[8]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 1, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[9]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 1, key{check.CodeRequesterDecides, check.SeverityWarning, "transitions[10]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 1, key{check.CodeRequesterDecides, check.SeverityWarning, "transitions[11]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 1, key{check.CodeRequesterDecides, check.SeverityWarning, "transitions[12]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 1, key{check.CodeRequesterDecides, check.SeverityWarning, "transitions[13]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 1, key{check.CodeRequesterDecides, check.SeverityWarning, "transitions[14]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 1, key{check.CodeRequesterDecides, check.SeverityWarning, "transitions[8]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 1, key{check.CodeRequesterDecides, check.SeverityWarning, "transitions[9]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 2, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[10]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 2, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[11]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 2, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[12]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 2, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[13]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 2, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[14]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 2, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[8]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 2, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[9]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 2, key{check.CodeRequesterDecides, check.SeverityWarning, "transitions[10]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 2, key{check.CodeRequesterDecides, check.SeverityWarning, "transitions[11]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 2, key{check.CodeRequesterDecides, check.SeverityWarning, "transitions[12]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 2, key{check.CodeRequesterDecides, check.SeverityWarning, "transitions[13]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 2, key{check.CodeRequesterDecides, check.SeverityWarning, "transitions[14]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 2, key{check.CodeRequesterDecides, check.SeverityWarning, "transitions[8]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"build_step", 2, key{check.CodeRequesterDecides, check.SeverityWarning, "transitions[9]", "field:f_owner, role:maintainer"}, whyBuildStep},
	{"equipment_request", 1, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[1]", "role:desk_agent"}, whyEquipment},
	{"equipment_request", 1, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[2]", "role:desk_agent"}, whyEquipment},
	{"equipment_request", 2, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[1]", "role:desk_agent"}, whyEquipment},
	{"equipment_request", 2, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[2]", "role:desk_agent"}, whyEquipment},
	{"fulfilment_task", 1, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[1]", "role:desk_agent"}, whyFulfilment},
	{"fulfilment_task", 1, key{check.CodeRequesterDecides, check.SeverityWarning, "transitions[1]", "role:desk_agent"}, whyFulfilment},
	{"incident", 2, key{check.CodeAgentDecides, check.SeverityError, "transitions[8]", ""}, whyIncidentCancel},
	{"incident", 2, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[7]", "role:desk_agent"}, whyIncidentNew},
	{"incident", 2, key{check.CodeRequesterDecides, check.SeverityError, "transitions[8]", ""}, whyIncidentCancel},
	{"incident", 3, key{check.CodeAgentDecides, check.SeverityError, "transitions[8]", ""}, whyIncidentCancel},
	{"incident", 3, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[7]", "role:desk_agent"}, whyIncidentNew},
	{"incident", 3, key{check.CodeRequesterDecides, check.SeverityError, "transitions[8]", ""}, whyIncidentCancel},
	{"incident", 4, key{check.CodeAgentDecides, check.SeverityError, "transitions[8]", ""}, whyIncidentCancel},
	{"incident", 4, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[7]", "role:desk_agent"}, whyIncidentNew},
	{"incident", 4, key{check.CodeRequesterDecides, check.SeverityError, "transitions[8]", ""}, whyIncidentCancel},
	{"incident", 5, key{check.CodeAgentDecides, check.SeverityError, "transitions[8]", ""}, whyIncidentCancel},
	{"incident", 5, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[7]", "role:desk_agent"}, whyIncidentNew},
	{"incident", 5, key{check.CodeRequesterDecides, check.SeverityError, "transitions[8]", ""}, whyIncidentCancel},
	{"incident", 6, key{check.CodeAgentDecides, check.SeverityError, "transitions[8]", ""}, whyIncidentCancel},
	{"incident", 6, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[7]", "role:desk_agent"}, whyIncidentNew},
	{"incident", 6, key{check.CodeRequesterDecides, check.SeverityError, "transitions[8]", ""}, whyIncidentCancel},
	{"incident", 7, key{check.CodeAgentDecides, check.SeverityError, "transitions[8]", ""}, whyIncidentCancel},
	{"incident", 7, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[7]", "role:desk_agent"}, whyIncidentNew},
	{"incident", 7, key{check.CodeRequesterDecides, check.SeverityError, "transitions[8]", ""}, whyIncidentCancel},
	{"incident", 8, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[7]", "role:desk_agent"}, whyIncidentNew},
	{"incident", 8, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[8]", "role:desk_agent"}, whyIncidentActive},
	{"incident", 8, key{check.CodeRequesterDecides, check.SeverityWarning, "transitions[8]", "role:desk_agent"}, whyIncidentActive},
	{"leave_balance", 1, key{check.CodeAgentDecides, check.SeverityWarning, "transitions[0]", "role:hr_admin"}, whyLeaveBalance},
	{"leave_balance", 1, key{check.CodeRequesterDecides, check.SeverityWarning, "transitions[0]", "role:hr_admin"}, whyLeaveBalance},
	{"access_request", 1, key{check.CodeColleagueEditsDraft, check.SeverityWarning, "states[0]", "scope:readers"}, whyDraft},
	{"access_request", 2, key{check.CodeColleagueEditsDraft, check.SeverityWarning, "states[0]", "scope:readers"}, whyDraft},
	{"vacation_request", 1, key{check.CodeColleagueEditsDraft, check.SeverityWarning, "states[0]", "scope:readers"}, whyDraft},
	{"vacation_request", 2, key{check.CodeColleagueEditsDraft, check.SeverityWarning, "states[0]", "scope:readers"}, whyDraft},
	{"vacation_request", 3, key{check.CodeColleagueEditsDraft, check.SeverityWarning, "states[0]", "scope:readers"}, whyDraft},
	{"vacation_request", 4, key{check.CodeColleagueEditsDraft, check.SeverityWarning, "states[0]", "scope:readers"}, whyDraft},
}

func TestTypesCleanOrExplained(t *testing.T) {
	types := loadDir(t, typesDir)
	r := runDir(t, typesDir)
	for _, ty := range types {
		var want []key
		for _, e := range typesFindings {
			if e.id == ty.ID && e.version == ty.Version {
				want = append(want, e.key)
			}
		}
		sameKeys(t, fmt.Sprintf("%s@%d", ty.ID, ty.Version), of(r, ty.ID, ty.Version), want)
	}
	for _, e := range typesFindings {
		if e.why == "" {
			t.Errorf("%s@%d %+v: no why", e.id, e.version, e.key)
		}
	}
	for _, f := range r.Findings {
		if f.Code == check.CodeUnchecked {
			t.Errorf("%s@%d: UNCHECKED %s", f.Type, f.Version, f.Path)
		}
	}
}
