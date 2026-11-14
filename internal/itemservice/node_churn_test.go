// Test for Step A2: node churn. Inherit on a narrowed container with no
// children removes its node, while first children created at the same moment
// need one. The child path that finds the node gone after waiting takes the
// container's row FOR SHARE and then FOR UPDATE; two such children could each
// hold the share lock and wait for the other's. Moves add more of this churn.
package itemservice_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// Inherit (removing the node) racing three first children, many times over:
// every write succeeds, and the stored visibility stays exact.
func TestNodeChurn_InheritRacingFirstChildren(t *testing.T) {
	k := itemservice.New(pool)
	admin := person("admin")
	for round := range 15 {
		space := createAs(t, k, admin, "space", &itemservice.Narrow{Readers: readers(admin)}, nil)

		start := make(chan struct{})
		errs := make(chan error, 4)
		var wg sync.WaitGroup
		wg.Add(4)
		go func() {
			defer wg.Done()
			<-start
			_, err := k.Update(testCtx(t), itemservice.UpdateRequest{
				IdempotencyKey: newKey(), Principal: admin, ItemID: space.ItemID,
				Policy: &itemservice.PolicyPatch{Inherit: true},
			})
			errs <- err
		}()
		for i := range 3 {
			go func() {
				defer wg.Done()
				<-start
				_, err := k.Create(testCtx(t), itemservice.CreateRequest{
					IdempotencyKey: newKey(), Principal: admin, TypeID: "ticket", TypeVersion: 1,
					ContainerID: &space.ItemID, Fields: map[string]any{"f_title": fmt.Sprintf("child %d", i)},
				})
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
		if n := count(t, `select count(*) from scopes where item_id = $1`, space.ItemID); n != 1 {
			t.Fatalf("round %d: the container with children has %d nodes, want 1", round, n)
		}
	}
	assertAccessMatchesRecompute(t)
}
