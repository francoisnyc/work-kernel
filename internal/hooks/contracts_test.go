// Tests for Ventoux V1, worker side: the catalog is the contracts
// (contracts.Default), and the worker holds every connector to its
// contract at run time, compiled in or remote alike.
//
//   - Every compiled-in action has a contract; the contracts without code
//     are the remote ones (hr.manager_of@1); Arity is the contract's.
//   - A check's deny reason must be one its contract declares. Remote
//     answers were checked against the descriptor before V1; compiled-in
//     ones now are too. An undeclared reason is ErrConnectorMismatch.
//   - Every output a connector answers must have its declared type (an enum
//     one of its values, a principal a principal ID): else
//     ErrConnectorMismatch.
//   - A remote descriptor must fit the contract (contracts.Matches): types,
//     format and enum when given, side effects, reasons among the
//     contract's. A misfit is ErrConnectorMismatch, and nothing is posted.
package hooks_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/hookproto"
	"github.com/francoisnyc/work-kernel/internal/hooks"
)

func TestContracts_TheCatalogIsTheContracts(t *testing.T) {
	var all, remote []string
	for _, c := range contracts.Default().All() {
		all = append(all, c.Action())
		r, w, ok := hooks.Arity(c.Action())
		if !ok || r != len(c.InputValues()) || w != len(c.OutputValues()) {
			t.Errorf("Arity(%s) = %d, %d, %v; want the contract's %d, %d", c.Action(), r, w, ok,
				len(c.InputValues()), len(c.OutputValues()))
		}
	}
	compiled := hooks.CompiledInForTest()
	for _, a := range compiled {
		if !slices.Contains(all, a) {
			t.Errorf("compiled-in action %s has no contract", a)
		}
	}
	for _, a := range all {
		if !slices.Contains(compiled, a) {
			remote = append(remote, a)
		}
	}
	if want := []string{"hr.manager_of@1"}; !reflect.DeepEqual(remote, want) {
		t.Errorf("contracts without code (remote) = %v, want %v", remote, want)
	}
	if len(all) == 0 {
		t.Error("no contracts")
	}
}

// stubServer answers every request with body, as JSON.
func stubServer(t *testing.T, body any) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A compiled-in check denying with a reason its contract does not declare
// is a mismatch, as a remote one's is.
func TestContracts_CompiledInReasons(t *testing.T) {
	erp := stubServer(t, map[string]any{"verdict": "deny", "reason": "made_up"})
	c := hooks.NewConnectors("http://crm.invalid", erp)
	_, err := c.Run(ctx(t), "erp.check_credit", "", []any{"cust-1", 100})
	if !errors.Is(err, hooks.ErrConnectorMismatch) || !errors.Is(err, hooks.ErrPermanent) {
		t.Errorf("deny made_up: error = %v, want ErrConnectorMismatch (permanent)", err)
	}
	declared := stubServer(t, map[string]any{"verdict": "deny", "reason": "over_credit_limit"})
	res, err := hooks.NewConnectors("http://crm.invalid", declared).Run(ctx(t), "erp.check_credit", "", []any{"cust-1", 100})
	if err != nil || res.Verdict != hooks.Deny || res.Reason != "over_credit_limit" {
		t.Errorf("deny over_credit_limit = %+v, %v; want the denial", res, err)
	}
}

// A compiled-in action's outputs are checked against their types: crm's
// plan is one of free, pro and enterprise.
func TestContracts_CompiledInOutputs(t *testing.T) {
	for _, plan := range []any{"gold", ""} {
		crm := stubServer(t, map[string]any{"plan": plan})
		_, err := hooks.NewConnectors(crm, "http://erp.invalid").Run(ctx(t), "crm.get_plan", "", []any{"cust-1"})
		if !errors.Is(err, hooks.ErrConnectorMismatch) || !errors.Is(err, hooks.ErrPermanent) {
			t.Errorf("plan %q: error = %v, want ErrConnectorMismatch (permanent)", plan, err)
		}
	}
	crm := stubServer(t, map[string]any{"plan": "pro"})
	res, err := hooks.NewConnectors(crm, "http://erp.invalid").Run(ctx(t), "crm.get_plan", "", []any{"cust-1"})
	if err != nil || !reflect.DeepEqual(res.Values, []any{"pro"}) {
		t.Errorf("plan pro = %+v, %v", res, err)
	}
}

// A remote connector's outputs are checked too: HR's manager is a
// principal.
func TestContracts_RemoteOutputs(t *testing.T) {
	for _, manager := range []any{42, "bob", nil} {
		f := newFake(t, hrDescriptor())
		f.answer = func(req hookproto.Request) (int, any) {
			return 200, hookproto.Response{Protocol: hookproto.Protocol, RequestID: req.RequestID,
				Outcome: hookproto.OutcomeCorrect, Patch: map[string]any{"manager": manager}, ConnectorVersion: "1.4.0"}
		}
		_, err := remoteHR(t, f).RunCall(ctx(t), "hr.manager_of", "hold:h-1", []any{nil, "user:ana"}, hrCall())
		if !errors.Is(err, hooks.ErrConnectorMismatch) || !errors.Is(err, hooks.ErrPermanent) {
			t.Errorf("manager %#v: error = %v, want ErrConnectorMismatch (permanent)", manager, err)
		}
	}
}

// A remote descriptor that does not fit the contract is a mismatch before
// anything is posted; one that fits, with or without the contract's
// additions, is called.
func TestContracts_RemoteDescriptor(t *testing.T) {
	withOp := func(change func(op *hookproto.Operation)) hookproto.Descriptor {
		d := hrDescriptor()
		op := d.Operations[0]
		props := map[string]hookproto.Property{}
		for k, v := range op.InputSchema.Properties {
			props[k] = v
		}
		op.InputSchema.Properties = props
		change(&op)
		d.Operations = []hookproto.Operation{op}
		return d
	}
	hr, ok := contracts.Default().Lookup("hr.manager_of@1")
	if !ok {
		t.Fatal("no contract for hr.manager_of@1")
	}
	fits := map[string]hookproto.Descriptor{
		"as before V1":        hrDescriptor(),
		"the contract itself": withOp(func(op *hookproto.Operation) { *op = hr.Operation }),
		"fewer reasons":       withOp(func(op *hookproto.Operation) { op.Errors = []string{"unknown_employee"} }),
	}
	for what, d := range fits {
		f := newFake(t, d)
		if _, err := remoteHR(t, f).RunCall(ctx(t), "hr.manager_of", "hold:h-1", []any{nil, "user:ana"}, hrCall()); err != nil {
			t.Errorf("%s: %v", what, err)
		}
	}
	misfits := map[string]hookproto.Descriptor{
		"an input of another type": withOp(func(op *hookproto.Operation) {
			op.InputSchema.Properties["createdBy"] = hookproto.Property{Type: hookproto.TypeList{"integer", "null"}}
		}),
		"another format": withOp(func(op *hookproto.Operation) {
			op.InputSchema.Properties["createdBy"] = hookproto.Property{Type: hookproto.TypeList{"string", "null"}, Format: "date"}
		}),
		"side effects": withOp(func(op *hookproto.Operation) { op.SideEffects = "some" }),
		"an undeclared reason": withOp(func(op *hookproto.Operation) {
			op.Errors = []string{"unknown_employee", "no_eligible_approver", "on_leave"}
		}),
	}
	for what, d := range misfits {
		f := newFake(t, d)
		_, err := remoteHR(t, f).RunCall(ctx(t), "hr.manager_of", "hold:h-1", []any{nil, "user:ana"}, hrCall())
		if !errors.Is(err, hooks.ErrConnectorMismatch) {
			t.Errorf("%s: error = %v, want ErrConnectorMismatch", what, err)
		}
		if _, posts := f.counts(); posts != 0 {
			t.Errorf("%s: %d posts, want none", what, posts)
		}
	}
}
