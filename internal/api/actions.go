package api

import (
	"context"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/hookproto"
	"github.com/francoisnyc/work-kernel/internal/item"
)

// ListActions serves the action catalogue (Ventoux V1): every action a
// Type's hook may name, with its contract. The catalogue is the kernel's
// own (contracts/, compiled in), the same in every namespace; it reads no
// Item and holds no Item data, so nothing here goes through
// policy.Project, as for ListTypes. Users and agents may call it;
// customers, and agents acting for one, may not: it describes the
// organisation's integrations.
func (s *server) ListActions(ctx context.Context, req *connect.Request[kernelv1.ListActionsRequest]) (*connect.Response[kernelv1.ListActionsResponse], error) {
	p := caller(ctx)
	if cerr := parentNamespace(p, req.Msg.Parent); cerr != nil {
		return nil, cerr
	}
	if item.IsKind(p.Actor, item.KindCustomer) || item.IsKind(p.OnBehalfOf, item.KindCustomer) {
		return nil, apiError(connect.CodePermissionDenied, "NOT_ALLOWED", nil)
	}
	all := contracts.Default().All()
	out := &kernelv1.ListActionsResponse{Actions: make([]*kernelv1.Action, 0, len(all))}
	for _, c := range all {
		out.Actions = append(out.Actions, actionOut(p.Namespace, c))
	}
	return connect.NewResponse(out), nil
}

func actionOut(ns string, c contracts.Contract) *kernelv1.Action {
	a := &kernelv1.Action{
		Name:        "namespaces/" + ns + "/actions/" + c.Action(),
		Action:      c.Action(),
		Connector:   c.Connector,
		Operation:   c.Name,
		Major:       int32(c.Major),
		Description: c.Description,
		Inputs:      valuesOut(c.InputValues()),
		Outputs:     valuesOut(c.OutputValues()),
		SideEffects: c.SideEffects == hookproto.SideEffectsSome,
		Risk:        riskOut[c.Risk],
		Reasons:     c.Errors,
		Digest:      c.Digest(),
	}
	for _, k := range c.Kinds {
		a.Kinds = append(a.Kinds, kindOut[k])
	}
	return a
}

var (
	kindOut = map[hookproto.Kind]kernelv1.HookKind{
		hookproto.KindCheck:  kernelv1.HookKind_HOOK_KIND_CHECK,
		hookproto.KindEnrich: kernelv1.HookKind_HOOK_KIND_ENRICH,
		hookproto.KindAction: kernelv1.HookKind_HOOK_KIND_ACTION,
	}
	riskOut = map[string]kernelv1.ActionRisk{
		hookproto.RiskLow:    kernelv1.ActionRisk_ACTION_RISK_LOW,
		hookproto.RiskMedium: kernelv1.ActionRisk_ACTION_RISK_MEDIUM,
		hookproto.RiskHigh:   kernelv1.ActionRisk_ACTION_RISK_HIGH,
	}
	trustOut = map[string]kernelv1.ActionInputTrust{
		hookproto.TrustAny:     kernelv1.ActionInputTrust_ACTION_INPUT_TRUST_ANY,
		hookproto.TrustTrusted: kernelv1.ActionInputTrust_ACTION_INPUT_TRUST_TRUSTED,
	}
	valueTypeOut = map[string]kernelv1.FieldType{
		contracts.TypeString:    kernelv1.FieldType_FIELD_TYPE_STRING,
		contracts.TypeInt:       kernelv1.FieldType_FIELD_TYPE_INT,
		contracts.TypeBool:      kernelv1.FieldType_FIELD_TYPE_BOOL,
		contracts.TypeEnum:      kernelv1.FieldType_FIELD_TYPE_ENUM,
		contracts.TypePrincipal: kernelv1.FieldType_FIELD_TYPE_PRINCIPAL,
		contracts.TypeDate:      kernelv1.FieldType_FIELD_TYPE_DATE,
		contracts.TypeDateTime:  kernelv1.FieldType_FIELD_TYPE_DATETIME,
	}
)

// valuesOut maps inputs or outputs; an output's trust stays UNSPECIFIED.
func valuesOut(vs []contracts.Value) []*kernelv1.ActionValue {
	var out []*kernelv1.ActionValue
	for _, v := range vs {
		out = append(out, &kernelv1.ActionValue{
			Name: v.Name, Type: valueTypeOut[v.Type], Options: v.Options, Nullable: v.Nullable,
			Description: v.Description, Trust: trustOut[v.Trust],
		})
	}
	return out
}
