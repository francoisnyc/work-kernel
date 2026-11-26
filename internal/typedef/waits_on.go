package typedef

// Track 1 item 5, step 5, "sent back to the requester": a state's
// `waits_on: requester` says that an Item in it waits on its requester,
// besides whoever its moves name. What it means is package policy's; this
// file refuses, at load, a value it does not know and a state where it
// would contradict the rest of the Type.

// WaitsOnRequester is the one value of a state's waits_on.
const WaitsOnRequester = "requester"

// checkWaitsOn validates every state's waits_on:
//
//   - the value: requester, and nothing else (INVALID_VALUE);
//   - the state: not in category done or canceled, which never wait
//     (CONFLICT); not claimed, since a claimed Item waits on its claimant
//     only (CONFLICT); not the initial state, or every new Item would wait
//     on the person who just made it (CONFLICT).
func (t *Type) checkWaitsOn(r reporter) {
	for i, s := range t.States {
		if s.WaitsOn == "" {
			continue
		}
		p := at("states", i, "waits_on")
		switch {
		case s.WaitsOn != WaitsOnRequester:
			r.fail(CodeInvalidValue, p, "Write requester", "state %s: waits_on is %q; the one value is requester", s.ID, s.WaitsOn)
		case s.Category == "done" || s.Category == "canceled":
			r.fail(CodeConflict, p, "Remove it: a done or canceled state waits on nobody",
				"state %s: waits_on, but its category %s never waits", s.ID, s.Category)
		case s.Claim != "":
			r.fail(CodeConflict, p, "Remove one: a claimed Item waits on its claimant only",
				"state %s: waits_on requester and claim %s at once", s.ID, s.Claim)
		case t.InitialState == s.ID:
			r.fail(CodeConflict, p, "Put it on a state Items are sent back to, not the one they start in",
				"state %s: the initial state may not wait on the requester; every new Item would wait on whoever made it", s.ID)
		}
	}
}
