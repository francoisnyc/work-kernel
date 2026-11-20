// Tests for Jira part 2, step 2: how "an Item was created inside me"
// reaches a container's workflow. The rule is the container's (a reaction
// on: created_inside), so the relay tells the CONTAINER: it sends the
// child's create, as a shape, to the container's workflow, starting one if
// none runs. The child's own outbox row carries it; only a child created in
// a container that declares such a reaction for its Type has one.
package relay_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

const insideDesk = `
id: rldesk
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
states:
  - {id: open,    category: todo}
  - {id: waiting, category: waiting}
transitions:
  - {from: open,    to: waiting}
  - {from: waiting, to: open}
reactions:
  - {id: reopen, kind: move, on: created_inside, of: [rlremark], when: 'child.f_public == true', to: open, from: [waiting]}
`

const insideRemark = `
id: rlremark
version: 1
fields:
  - {id: f_body,   type: string}
  - {id: f_public, type: bool}
`

func registerInside(t *testing.T, k *itemservice.Service) {
	t.Helper()
	for _, src := range []string{insideRemark, insideDesk} {
		ty, err := typedef.Parse([]byte(src))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if err := k.RegisterType(testCtx(t), ty); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
}

func makeItem(t *testing.T, k *itemservice.Service, typ string, in *uuid.UUID, fields map[string]any, corr string) uuid.UUID {
	t.Helper()
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: alice, TypeID: typ, TypeVersion: 1, ContainerID: in, Fields: fields,
		Attribution: item.Attribution{CorrelationID: corr},
	})
	if err != nil {
		t.Fatalf("create %s: %v", typ, err)
	}
	return res.ItemID
}

// A remark created in a desk is delivered to the DESK's workflow, with
// start: the desk may have no run (waiting has no behavior of its own).
// The delivery is a shape: the child's ID, Type and create event, no field.
func TestRelay_AChildCreatedInsideGoesToItsContainersWorkflow(t *testing.T) {
	r, fake, _ := newRelay(t)
	k := itemservice.New(pool)
	registerInside(t, k)
	desk := makeItem(t, k, "rldesk", nil, map[string]any{"f_title": "t"}, "corr-desk")
	runOnce(t, r) // the desk's own create

	remark := makeItem(t, k, "rlremark", &desk, map[string]any{"f_public": true, "f_body": "secret words"}, "corr-remark")
	runOnce(t, r)

	var toDesk []call
	for _, c := range fake.callsFor(desk) {
		if c.Child != nil {
			toDesk = append(toDesk, c)
		}
	}
	if len(toDesk) != 1 {
		t.Fatalf("deliveries of a child to the desk = %+v, want one", toDesk)
	}
	c := toDesk[0]
	want := wf.ChildEvent{ChildID: remark.String(), TypeID: "rlremark", Seq: 1, CorrelationID: "corr-remark"}
	got := *c.Child
	if at := eventTime(t, remark, 1); !got.At.Equal(at) {
		t.Errorf("Child.At = %v, want the remark's create time %v", got.At, at)
	}
	got.At = want.At
	if got != want || !c.Start || c.UpdateID != remark.String()+":1:inside" || c.Event.Seq != 0 {
		t.Errorf("delivery = %+v (child %+v), want the child's shape, with start, update ID <child>:1:inside, no event", c, *c.Child)
	}
	// The remark's Type has nothing listening: nothing is sent under its own ID.
	if calls := fake.callsFor(remark); len(calls) != 0 {
		t.Errorf("deliveries to the remark itself = %+v, want none", calls)
	}
	if left := pending(t, remark); len(left) != 0 {
		t.Errorf("undelivered rows of the remark = %v, want none", left)
	}
}

// The row is marked delivered only once the container's workflow has taken
// it: a failure keeps it, and it is sent again under the same update ID.
func TestRelay_AChildDeliveryThatFailsIsRetried(t *testing.T) {
	r, fake, clock := newRelay(t)
	k := itemservice.New(pool)
	registerInside(t, k)
	desk := makeItem(t, k, "rldesk", nil, nil, "")
	runOnce(t, r)
	remark := makeItem(t, k, "rlremark", &desk, map[string]any{"f_public": true}, "")

	fails := 1
	fake.answer = func(c call) error {
		if c.Child != nil && fails > 0 {
			fails--
			return errors.New("temporal is unreachable")
		}
		return nil
	}
	runOnce(t, r)
	if left := pending(t, remark); len(left) != 1 {
		t.Fatalf("undelivered rows of the remark after a failure = %v, want its create", left)
	}
	clock.advance(2 * time.Second) // past the backoff
	runOnce(t, r)
	if left := pending(t, remark); len(left) != 0 {
		t.Errorf("undelivered rows after the retry = %v, want none", left)
	}
	var ids []string
	for _, c := range fake.callsFor(desk) {
		if c.Child != nil {
			ids = append(ids, c.UpdateID)
		}
	}
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Errorf("update IDs of the two attempts = %v, want the same ID twice", ids)
	}
}

// A remark created anywhere else has no outbox row: the relay never hears
// of it, and its container gets nothing.
func TestRelay_AChildElsewhereCostsNothing(t *testing.T) {
	r, fake, _ := newRelay(t)
	k := itemservice.New(pool)
	registerInside(t, k)
	box := makeItem(t, k, "rlremark", nil, nil, "") // a plain Item as a container
	inBox := makeItem(t, k, "rlremark", &box, map[string]any{"f_public": true}, "")
	subDesk := makeItem(t, k, "rldesk", nil, nil, "")
	deskInDesk := makeItem(t, k, "rldesk", &subDesk, nil, "") // not one of `of`
	runOnce(t, r)
	for name, id := range map[string]uuid.UUID{"the box": box, "the remark in it": inBox} {
		if calls := fake.callsFor(id); len(calls) != 0 {
			t.Errorf("deliveries for %s = %+v, want none", name, calls)
		}
	}
	for _, c := range fake.callsFor(subDesk) {
		if c.Child != nil {
			t.Errorf("a desk created in a desk was delivered as a child: %+v", *c.Child)
		}
	}
	_ = deskInDesk
}
