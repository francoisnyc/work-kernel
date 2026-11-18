// Tests for the consolidation after Step 2, item 6: two load rules for
// kernel-resolved links.
//
//   - resolve.on_enter may not be the initial state: Create never resolves
//     links, so the link would stay unset on every Item.
//   - A held reaction on a resolve link may leave only from that link's
//     on_enter state: the only state where the link is sure to be set.
//     Links people set are exempt.
package typedef_test

import "testing"

const pledgeLink = `  - {name: jar, inverse: pledges, targets: [jar], max: one, writable_by: kernel,
     resolve: {on_enter: filed, field: f_holder, equals: creator, state: active}}`

func TestResolveOnTheInitialState_Refused(t *testing.T) {
	mustRefuse(t, "resolve on entering the initial state",
		swap(t, pledgeSrc, "on_enter: filed", "on_enter: open"), "initial")
}

func TestHeldReactionLeavingAStateItsLinkIsNotResolvedIn_Refused(t *testing.T) {
	// review: entered from filed, the link resolved on entering filed; a
	// pledge may also be paid from review.
	withReview := swap(t, swap(t, swap(t, pledgeSrc,
		"  - {id: paid,    category: done}", "  - {id: review,  category: active}\n  - {id: paid,    category: done}"),
		"  - {from: filed, to: paid}", "  - {from: filed, to: paid}\n  - {from: filed, to: review}\n  - {from: review, to: paid}"),
		"from: [filed], link: jar", "from: [filed, review], link: jar")
	mustRefuse(t, "a held reaction leaving review, where its link is not resolved", withReview, "review")
	// A link people set is exempt: it can be set in any state.
	mustParse(t, swap(t, withReview, pledgeLink, "  - {name: jar, inverse: pledges, targets: [jar], max: one}"))
}
