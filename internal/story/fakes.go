package story

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/hookproto"
	"github.com/francoisnyc/work-kernel/internal/hooks"
)

// fake is one connector@major of a story: it answers every operation of
// that connector the story fakes, by its rules. It is a hooks.Connector,
// reached through hooks.Connectors.WithConnector (the Go binding), so the
// worker holds its answers to the contracts as it holds any connector's.
type fake struct {
	h         *harness
	connector string // hr@1
}

func (f *fake) Handle(ctx context.Context, req hookproto.Request) (hookproto.Response, error) {
	h := f.h
	connector, major, _ := strings.Cut(req.Hook.Connector, "@")
	action := fmt.Sprintf("%s.%s@%s", connector, req.Hook.Operation, major)
	now := h.now()
	_, hookID, _ := strings.Cut(req.Hook.Binding, "#")
	itemID, _ := uuid.Parse(req.Item.ID)
	call := Call{At: now, Action: action, ItemID: itemID, Hook: hookID, Inputs: req.Inputs}

	i := slices.IndexFunc(h.story.Fakes, func(fk Fake) bool { return fk.Action == action })
	contract, _ := h.env.catalog.Lookup(action)
	var rule *Rule
	if i >= 0 {
		for j := range h.story.Fakes[i].Rules {
			if r := &h.story.Fakes[i].Rules[j]; h.matches(r, contract, req.Inputs, now) {
				rule = r
				break
			}
		}
	}
	if rule == nil {
		h.recordCall(call, req.IdempotencyKey)
		h.failAt(now, "no rule of %s's fake answers %s for %s: %s", action, hookID, h.nameOf(itemID),
			h.renderer().contractValues(req.Inputs, contract.InputValues()))
		return hookproto.Response{}, fmt.Errorf("%w: story: no fake answer for %s", hooks.ErrPermanent, action)
	}
	call.Answer = rule.Kind
	res := hookproto.Response{Protocol: hookproto.Protocol, RequestID: req.RequestID}
	switch rule.Kind {
	case Allow:
		res.Outcome = hookproto.OutcomeAllow
	case Deny:
		res.Outcome, res.Reason = hookproto.OutcomeDeny, &hookproto.Reason{Code: rule.Reason}
	case Correct, OK:
		res.Outcome = hookproto.OutcomeCorrect
		if rule.Kind == OK {
			res.Outcome = hookproto.OutcomeOK
		}
		outs := contract.OutputValues()
		for _, a := range rule.Values {
			k := slices.IndexFunc(outs, func(v contracts.Value) bool { return v.Name == a.Field })
			if k < 0 {
				continue // Check refused it already
			}
			x, err := h.conv.value(a.Value, contractSlot(outs[k]))
			if err != nil {
				continue
			}
			if res.Patch == nil {
				res.Patch = map[string]any{}
			}
			res.Patch[a.Field] = x
		}
		call.Outputs = res.Patch
		call.SideEffect = rule.Kind == OK && contract.SideEffects == "some"
	case Fail:
		h.recordCall(call, req.IdempotencyKey)
		switch rule.Failure {
		case FailInvalid:
			return hookproto.Response{}, fmt.Errorf("%w: story: %s fails invalid", hooks.ErrPermanent, action)
		case FailUnauthorized:
			return hookproto.Response{}, fmt.Errorf("%w: story: %s fails unauthorized", hooks.ErrConnectorUnauthorized, action)
		case FailKeyConflict:
			return hookproto.Response{}, fmt.Errorf("%w: story: %s fails key_conflict", hooks.ErrKeyConflict, action)
		}
		return hookproto.Response{}, errors.New("story: " + action + " is down (fail transient)")
	}
	h.recordCall(call, req.IdempotencyKey)
	return res, nil
}

// matches reports whether a rule answers a call with these inputs, now.
func (h *harness) matches(r *Rule, contract contracts.Contract, inputs map[string]any, now time.Time) bool {
	if !r.From.IsZero() && now.Before(r.From) {
		return false
	}
	if !r.Until.IsZero() && !now.Before(r.Until) {
		return false
	}
	ins := contract.InputValues()
	for _, a := range r.When {
		k := slices.IndexFunc(ins, func(v contracts.Value) bool { return v.Name == a.Field })
		if k < 0 {
			return false
		}
		sl := contractSlot(ins[k])
		want, err := h.conv.value(a.Value, sl)
		if err != nil {
			return false
		}
		rd := h.renderer()
		if rd.value(want, sl) != rd.value(inputs[a.Field], sl) {
			return false
		}
	}
	return true
}
