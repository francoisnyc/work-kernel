package typedef

// Track 1 item 5, step 5, "sent back to the requester": a state's
// `waits_on: requester` says that an Item in it waits on its requester,
// besides whoever its moves name.
//
// STUB: parsed and stored; nothing is checked at load yet.

// WaitsOnRequester is the one value of a state's waits_on.
const WaitsOnRequester = "requester"

func (t *Type) checkWaitsOn(r reporter) {}
