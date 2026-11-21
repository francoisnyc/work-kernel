// Adversarial review of Jira part 2, step 4a (docs/reviews/2026-11-21-customers.md).
package typedef_test

import "testing"

// Hardening. A rollup a customer sees counts over a link name: when the
// link is not listed for them, the count still tells them the hidden links
// exist (how many, in which states). A Type should not be able to list such
// a rollup without its link: refuse it at load.
func TestReview_CustomerFields_ARollupOverAnUnlistedLink_IsRefusedAtLoad(t *testing.T) {
	mustRefuse(t, "a listed rollup over an unlisted link", stub("customer_fields: [f_title, progress]"), "blocks")
}
