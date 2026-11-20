// The etag step, the API's half of "no new event kind without a decision":
// every kind of event the API can show (eventKinds) has a decision in the
// etag rule (itemservice.DecidedOps), and the rule decides nothing the API
// cannot show. itemservice's table test holds the decisions themselves.
// An internal test (package api) because eventKinds is not exported.
package api

import (
	"maps"
	"slices"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func TestEventKinds_EachHasAnEtagDecision(t *testing.T) {
	shown := slices.Sorted(maps.Keys(eventKinds))
	decided := itemservice.DecidedOps()
	for _, op := range shown {
		if !slices.Contains(decided, op) {
			t.Errorf("event kind %q has no etag decision: add it to the rule (internal/itemservice/content.go) and to its table test", op)
		}
	}
	for _, op := range decided {
		if !slices.Contains(shown, op) {
			t.Errorf("the etag rule decides %q, a kind the API does not show", op)
		}
	}
}
