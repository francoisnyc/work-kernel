// Tests for the PR #5 follow-up: Items of a Type without a lifecycle live in
// the Type's one implicit state.
package itemservice_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// registerMessageType registers a Type with fields and no states. Registering
// the same definition again is a no-op, so every test may call it.
func registerMessageType(t *testing.T, k *itemservice.Service) {
	t.Helper()
	ty, err := typedef.Parse([]byte(`
id: message
version: 1
fields:
  - {id: f_body, type: string}
`))
	if err != nil {
		t.Fatalf("parse message Type: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register message Type: %v", err)
	}
}

func createMessage(t *testing.T, k *itemservice.Service) uuid.UUID {
	t.Helper()
	registerMessageType(t, k)
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: alice, TypeID: "message", TypeVersion: 1,
		Fields: map[string]any{"f_body": "hello"},
	})
	if err != nil {
		t.Fatalf("create message: %v", err)
	}
	return res.ItemID
}

// An Item of a Type without states is created in the implicit state, and can
// be updated like any other Item.
func TestCreate_TypeWithoutStates_UsesTheImplicitState(t *testing.T) {
	k := itemservice.New(pool)
	id := createMessage(t, k)

	if got := get(t, k, id).State; got != "default" {
		t.Errorf("state = %q, want default", got)
	}

	update(t, k, id, map[string]any{"f_body": "hello, world"})
	got := get(t, k, id)
	if got.State != "default" || got.Fields["f_body"] != "hello, world" || got.ItemSeq != 2 {
		t.Errorf("after update: state %q, fields %v, item_seq %d; want default, the new body, 2",
			got.State, got.Fields, got.ItemSeq)
	}
}

// With one state and no transitions there is nowhere to go: every transition
// is rejected with a reason and writes nothing.
func TestTransition_TypeWithoutStates_IsRejected(t *testing.T) {
	k := itemservice.New(pool)
	id := createMessage(t, k)

	for _, to := range []string{"default", "done"} {
		t.Run("to "+to, func(t *testing.T) {
			_, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
				IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: to,
			})
			if !errors.Is(err, itemservice.ErrRejected) {
				t.Fatalf("Transition error = %v, want ErrRejected", err)
			}
			if !strings.Contains(err.Error(), to) {
				t.Errorf("error %q does not mention %q", err, to)
			}
			assertNothingWrittenNoOutbox(t, id, 1) // a message has no listeners (Step 5)
		})
	}
}
