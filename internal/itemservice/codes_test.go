// Tests for Step 0, part 1: refusals the API maps to codes carry them as
// typed errors, never as text to parse.
//
//   - *RejectedError: the Type's rules refuse valid input (ErrRejected). Code
//     is a stable snake_case code; the API sends it as UPPER_SNAKE.
//   - *InvalidError: the request is malformed (ErrInvalid). Field names the
//     field at fault; for a field value, its field ID.
//   - *HeldError: an open hold refuses the write (ErrHeld, an ErrStale).
//     HoldID names the hold.
//
// An unknown field or a value of the wrong type is now ErrInvalid, not
// ErrRejected: it is a malformed request, not a rule saying no.
package itemservice_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func assertRejectedCode(t *testing.T, what string, err error, code string) {
	t.Helper()
	var rej *itemservice.RejectedError
	if !errors.As(err, &rej) {
		t.Fatalf("%s: error = %v, want a *RejectedError", what, err)
	}
	if !errors.Is(err, itemservice.ErrRejected) {
		t.Errorf("%s: error %v does not match ErrRejected", what, err)
	}
	if rej.Code != code {
		t.Errorf("%s: code = %q, want %q", what, rej.Code, code)
	}
}

func assertInvalidField(t *testing.T, what string, err error, field string) {
	t.Helper()
	var inv *itemservice.InvalidError
	if !errors.As(err, &inv) {
		t.Fatalf("%s: error = %v, want an *InvalidError", what, err)
	}
	if !errors.Is(err, itemservice.ErrInvalid) || errors.Is(err, itemservice.ErrRejected) {
		t.Errorf("%s: error %v must match ErrInvalid and not ErrRejected", what, err)
	}
	if inv.Field != field {
		t.Errorf("%s: field = %q, want %q", what, inv.Field, field)
	}
}

// Every way the Type's rules refuse a transition has its code.
func TestRejection_TransitionCodes(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]any // the ticket's fields; it is in state "open"
		to     string
		code   string
	}{
		{"undeclared move", map[string]any{"f_assignee": "user-bob"}, "resolved", "no_transition"},
		{"unknown state", nil, "limbo", "no_transition"},
		{"same state", nil, "open", "no_transition"},
		{"guard fails", nil, "in_progress", "guard_failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := itemservice.New(pool)
			id := createTicket(t, k, c.fields)
			_, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
				IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: c.to,
			})
			assertRejectedCode(t, "Transition", err, c.code)
		})
	}
}

func TestRejection_MissingRequiredField(t *testing.T) {
	k := itemservice.New(pool)
	id := createTicket(t, k, map[string]any{"f_assignee": "user-bob"})
	transition(t, k, id, "in_progress")
	_, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: "resolved",
	})
	assertRejectedCode(t, "Transition without f_resolution", err, "missing_fields")
}

func TestRejection_NotTheInitialState(t *testing.T) {
	k := itemservice.New(pool)
	req := newRequest()
	req.State = "resolved"
	_, err := k.Create(testCtx(t), req)
	assertRejectedCode(t, "Create in resolved", err, "not_initial_state")
}

func TestRejection_HopLimit(t *testing.T) {
	k := itemservice.New(pool)
	id, _ := v3Ticket(t, k)
	over := v3Cause(getPlanV3, id, 1)
	over.Hop = itemservice.MaxHop + 1
	_, err := k.Internal().Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: getPlanV3, ItemID: id,
		Fields: map[string]any{"f_plan": "pro"}, Attribution: over,
	})
	assertRejectedCode(t, "Update past the hop limit", err, "hop_limit")
}

// A field the Type does not declare, or a value of the wrong type, is a
// malformed request naming the field, on every write that takes fields.
func TestInvalid_FieldErrorsNameTheField(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]any
		field  string
	}{
		{"unknown field", map[string]any{"f_ghost": "x"}, "f_ghost"},
		{"number for a string", map[string]any{"f_title": 42}, "f_title"},
		{"string for an int", map[string]any{"f_priority": "high"}, "f_priority"},
		{"string for a bool", map[string]any{"f_urgent": "yes"}, "f_urgent"},
		{"value outside the enum", map[string]any{"f_resolution": "magic"}, "f_resolution"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := testCtx(t)
			k := itemservice.New(pool)

			req := newRequest()
			req.Fields = c.fields
			_, err := k.Create(ctx, req)
			assertInvalidField(t, "Create", err, c.field)

			id := createTicket(t, k, map[string]any{"f_assignee": "user-bob"})
			_, err = k.Update(ctx, itemservice.UpdateRequest{
				IdempotencyKey: newKey(), Principal: alice, ItemID: id, Fields: c.fields,
			})
			assertInvalidField(t, "Update", err, c.field)

			_, err = k.Transition(ctx, itemservice.TransitionRequest{
				IdempotencyKey: newKey(), Principal: alice, ItemID: id, ToState: "in_progress", Fields: c.fields,
			})
			assertInvalidField(t, "Transition", err, c.field)
			assertNothingWritten(t, id, 1)
		})
	}
}

// A refusal by an open hold names the hold, and is still an ErrStale.
func TestHeld_ErrorNamesTheHold(t *testing.T) {
	k := itemservice.New(pool)
	id, ana := v3Ticket(t, k)
	req := requestHold(t, k, ana, id)

	_, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: ana, ItemID: id, Fields: map[string]any{"f_amount": 5},
	})
	var held *itemservice.HeldError
	if !errors.As(err, &held) {
		t.Fatalf("Update of a frozen field: error = %v, want a *HeldError", err)
	}
	if !errors.Is(err, itemservice.ErrHeld) || !errors.Is(err, itemservice.ErrStale) {
		t.Errorf("error %v must match ErrHeld and ErrStale", err)
	}
	if held.HoldID != req.HoldID || held.HoldID == uuid.Nil {
		t.Errorf("HoldID = %s, want %s", held.HoldID, req.HoldID)
	}
}
