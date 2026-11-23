// Tests for Ventoux V1: ListActions serves the action catalogue, every
// action a Type's hook may name with its contract, so an authoring tool
// knows the menu it may choose from. The catalogue is the kernel's
// (contracts/), the same in every namespace; it holds no Item data, so
// nothing goes through policy.Project. Users and agents (in a session) may
// call it; customers, and agents acting for one, may not.
package api_test

import (
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/gen/kernel/v1/kernelv1connect"
	"github.com/francoisnyc/work-kernel/internal/contracts"
)

func listActions(c kernelv1connect.ItemServiceClient, t *testing.T, parent string) (*kernelv1.ListActionsResponse, error) {
	t.Helper()
	res, err := c.ListActions(testCtx(t), connect.NewRequest(&kernelv1.ListActionsRequest{Parent: parent}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func actionsAs(t *testing.T, who, ns string) map[string]*kernelv1.Action {
	t.Helper()
	res, err := listActions(as(who), t, "namespaces/"+ns)
	if err != nil {
		t.Fatalf("ListActions as %s: %v", who, err)
	}
	out := map[string]*kernelv1.Action{}
	var order []string
	for _, a := range res.Actions {
		out[a.Action] = a
		order = append(order, a.Action)
	}
	want := []string{"crm.get_plan@1", "email.notify@1", "erp.check_credit@1", "erp.create_invoice@1",
		"erp.get_credit_status@1", "hr.manager_of@1"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("actions = %v, want %v (ordered by action)", order, want)
	}
	return out
}

// One action in full: hr.manager_of@1, as its contract says.
func TestListActions_AnActionInFull(t *testing.T) {
	got := actionsAs(t, "ana", "acme")["hr.manager_of@1"]
	contract, _ := contracts.Default().Lookup("hr.manager_of@1")
	in := contract.InputValues()
	out := contract.OutputValues()
	if len(in) != 2 || len(out) != 1 {
		t.Fatalf("test bug: the contract has %d inputs and %d outputs", len(in), len(out))
	}
	want := &kernelv1.Action{
		Name: "namespaces/acme/actions/hr.manager_of@1", Action: "hr.manager_of@1",
		Connector: "hr", Operation: "manager_of", Major: 1,
		Description: contract.Description,
		Kinds:       []kernelv1.HookKind{kernelv1.HookKind_HOOK_KIND_CHECK},
		Inputs: []*kernelv1.ActionValue{
			{Name: "onBehalfOf", Type: kernelv1.FieldType_FIELD_TYPE_PRINCIPAL, Nullable: true,
				Description: in[0].Description, Trust: kernelv1.ActionInputTrust_ACTION_INPUT_TRUST_TRUSTED},
			{Name: "createdBy", Type: kernelv1.FieldType_FIELD_TYPE_PRINCIPAL, Nullable: true,
				Description: in[1].Description, Trust: kernelv1.ActionInputTrust_ACTION_INPUT_TRUST_TRUSTED},
		},
		Outputs: []*kernelv1.ActionValue{
			{Name: "manager", Type: kernelv1.FieldType_FIELD_TYPE_PRINCIPAL, Description: out[0].Description},
		},
		SideEffects: false,
		Risk:        kernelv1.ActionRisk_ACTION_RISK_LOW,
		Reasons:     []string{"unknown_employee", "no_eligible_approver"},
		Digest:      contract.Digest(),
	}
	if !proto.Equal(got, want) {
		t.Errorf("hr.manager_of@1 =\n%v\nwant\n%v", got, want)
	}
	if strings.TrimSpace(got.GetDescription()) == "" {
		t.Error("no description")
	}
}

// The other kinds of value and action: an enum output and an enrich input
// (any), an int input, an action with side effects and high risk, one with
// no outputs.
func TestListActions_TheOthers(t *testing.T) {
	all := actionsAs(t, "ana", "acme")
	plan := all["crm.get_plan@1"]
	if out := plan.GetOutputs(); len(out) != 1 || out[0].Type != kernelv1.FieldType_FIELD_TYPE_ENUM ||
		!reflect.DeepEqual(out[0].Options, []string{"free", "pro", "enterprise"}) ||
		out[0].Trust != kernelv1.ActionInputTrust_ACTION_INPUT_TRUST_UNSPECIFIED {
		t.Errorf("crm.get_plan's output = %v; want plan, an enum of free, pro, enterprise", out)
	}
	if in := plan.GetInputs(); len(in) != 1 || in[0].Type != kernelv1.FieldType_FIELD_TYPE_STRING || in[0].Nullable ||
		in[0].Trust != kernelv1.ActionInputTrust_ACTION_INPUT_TRUST_ANY {
		t.Errorf("crm.get_plan's input = %v; want customerId, a string, any source", in)
	}
	if k := plan.GetKinds(); !reflect.DeepEqual(k, []kernelv1.HookKind{kernelv1.HookKind_HOOK_KIND_ENRICH}) {
		t.Errorf("crm.get_plan's kinds = %v", k)
	}
	credit := all["erp.check_credit@1"]
	if in := credit.GetInputs(); len(in) != 2 || in[1].Name != "amount" || in[1].Type != kernelv1.FieldType_FIELD_TYPE_INT {
		t.Errorf("erp.check_credit's inputs = %v; want amount, an int, second", in)
	}
	invoice := all["erp.create_invoice@1"]
	if !invoice.SideEffects || invoice.Risk != kernelv1.ActionRisk_ACTION_RISK_HIGH ||
		!reflect.DeepEqual(invoice.Kinds, []kernelv1.HookKind{kernelv1.HookKind_HOOK_KIND_ACTION}) {
		t.Errorf("erp.create_invoice: side effects %v, risk %v, kinds %v; want true, HIGH, ACTION",
			invoice.SideEffects, invoice.Risk, invoice.Kinds)
	}
	mail := all["email.notify@1"]
	if len(mail.Outputs) != 0 || mail.Risk != kernelv1.ActionRisk_ACTION_RISK_MEDIUM || len(mail.Reasons) != 0 {
		t.Errorf("email.notify: outputs %v, risk %v, reasons %v; want none, MEDIUM, none", mail.Outputs, mail.Risk, mail.Reasons)
	}
	for name, a := range all {
		c, _ := contracts.Default().Lookup(name)
		if a.Digest != c.Digest() || a.Digest == "" {
			t.Errorf("%s: digest %q, want the contract's %q", name, a.Digest, c.Digest())
		}
	}
}

// The catalogue is the same in every namespace; names are the caller's.
func TestListActions_EveryNamespace(t *testing.T) {
	bea := actionsAs(t, "bea", "globex")["hr.manager_of@1"]
	ana := actionsAs(t, "ana", "acme")["hr.manager_of@1"]
	if bea.Name != "namespaces/globex/actions/hr.manager_of@1" {
		t.Errorf("name = %q in globex", bea.Name)
	}
	bea.Name = ana.Name
	if !proto.Equal(bea, ana) {
		t.Errorf("globex's hr.manager_of@1 differs from acme's:\n%v\n%v", bea, ana)
	}
}

// The parent is the caller's namespace, as for ListTypes.
func TestListActions_Parent(t *testing.T) {
	_, err := listActions(as("ana"), t, "namespaces/globex")
	notFound(t, "another namespace", err)
	_, err = listActions(as("ana"), t, "acme")
	invalid(t, "a malformed parent", err, "parent")
}

// Users and agents in a session may call it, with no particular power;
// customers, and agents acting for one, may not.
func TestListActions_WhoMayCall(t *testing.T) {
	for what, c := range map[string]kernelv1connect.ItemServiceClient{
		"a user":                            as("bob"),
		"an agent for a user, comment only": asAgent("bot", startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "comment")),
	} {
		if _, err := listActions(c, t, "namespaces/acme"); err != nil {
			t.Errorf("%s: %v", what, err)
		}
	}
	for what, c := range map[string]kernelv1connect.ItemServiceClient{
		"a customer":              asCustomer("kim", ""),
		"an agent for a customer": asCustomer("portal-bot", portalSession(t)),
	} {
		_, err := listActions(c, t, "namespaces/acme")
		apiError(t, what, err, connect.CodePermissionDenied, "NOT_ALLOWED")
	}
}
