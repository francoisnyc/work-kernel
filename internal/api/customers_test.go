// Tests for Jira part 2, step 4a, through the public API: customers.
//
//   - GetType shows own, shared and customer_fields.
//   - A customer's Item carries only its customer fields and no etag.
//   - Not found is not found: a hidden Item and a missing one give the
//     same bytes, by name and by key (a search on a portal's f_key).
//   - A refusal tells a customer nothing more: PERMISSION_DENIED /
//     NOT_ALLOWED with no metadata.
//   - requester on CreateItem: a person opens an incident for a customer;
//     every refusal is INVALID_ARGUMENT on requester, in the same bytes
//     whether the customer is hidden from the portal or never existed.
//   - A portal bot acts for a customer with the fixed powers only.
package api_test

import (
	"slices"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/gen/kernel/v1/kernelv1connect"
	"github.com/francoisnyc/work-kernel/internal/devtoken"
)

const (
	deskV2     = "namespaces/acme/types/desk/versions/2"
	incidentV2 = "namespaces/acme/types/incident/versions/2"
	replyV1    = "namespaces/acme/types/reply/versions/1"
	noteV1     = "namespaces/acme/types/note/versions/1"
)

// asCustomer is a client calling as kim, lee or (with a session) the portal bot.
func asCustomer(name, session string) kernelv1connect.ItemServiceClient {
	i := slices.IndexFunc(customerTokens, func(tk devtoken.Token) bool { return tk.Name == name })
	if i < 0 {
		panic("no customer token named " + name)
	}
	return withToken(customerTokens[i].Token, sessionHeader(session))
}

// portal is a desk@2 made by hana, with sam and hana as desk agents. hana
// holds sla_admin too: only its holders narrow a desk of any version, as
// desk@3 says (the latest version's placement rule).
func portal(t *testing.T) string {
	t.Helper()
	if _, err := pool.Exec(testCtx(t), `insert into principal_roles (namespace_id, principal, role)
		values ('acme', 'user:sam', 'desk_agent'), ('acme', 'user:hana', 'desk_agent'), ('acme', 'user:hana', 'sla_admin')
		on conflict do nothing`); err != nil {
		t.Fatalf("grant desk_agent and sla_admin: %v", err)
	}
	return mkIn(t, "hana", deskV2, "", map[string]any{"f_key": "DESK-" + word(), "f_name": "portal " + word()})
}

func customerCreates(t *testing.T, c kernelv1connect.ItemServiceClient, typ, container string, f map[string]any) (*kernelv1.CreateItemResponse, error) {
	t.Helper()
	res, err := c.CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: typ, Container: container, Fields: fields(t, f),
	}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func kimsIncident(t *testing.T, desk string) string {
	t.Helper()
	res, err := customerCreates(t, asCustomer("kim", ""), incidentV2, desk, map[string]any{"f_title": "VPN " + word(), "f_priority": "P3"})
	if err != nil {
		t.Fatalf("kim opens an incident: %v", err)
	}
	return res.Item.Name
}

func TestGetType_ShowsCustomerDeclarations(t *testing.T) {
	for name, want := range map[string]func(*kernelv1.Type) bool{
		deskV2: func(ty *kernelv1.Type) bool {
			return slices.Equal(ty.Own, []string{"incident"}) && len(ty.Shared) == 0 && len(ty.CustomerFields) == 2
		},
		incidentV2: func(ty *kernelv1.Type) bool {
			return slices.Equal(ty.Shared, []string{"reply"}) && slices.Contains(ty.CustomerFields, "f_title") &&
				!slices.Contains(ty.CustomerFields, "f_assignee")
		},
		replyV1: func(ty *kernelv1.Type) bool { return slices.Equal(ty.CustomerFields, []string{"f_body"}) },
		noteV1:  func(ty *kernelv1.Type) bool { return len(ty.Own)+len(ty.Shared)+len(ty.CustomerFields) == 0 },
	} {
		res, err := asCustomer("kim", "").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: name}))
		if err != nil {
			t.Errorf("kim reads %s: %v (Types are readable by anyone in the namespace)", name, err)
			continue
		}
		if !want(res.Msg.Type) {
			t.Errorf("%s: own %v, shared %v, customer_fields %v", name, res.Msg.Type.Own, res.Msg.Type.Shared, res.Msg.Type.CustomerFields)
		}
	}
}

func TestCustomer_API_OnlyCustomerFields_NoEtag(t *testing.T) {
	desk := portal(t)
	inc := kimsIncident(t, desk)
	if _, err := as("sam-acme").UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: inc, RequestId: newID(), Fields: fields(t, map[string]any{"f_assignee": "user:sam"})})); err != nil {
		t.Fatalf("sam assigns: %v", err)
	}
	res, err := asCustomer("kim", "").GetItem(testCtx(t), connect.NewRequest(&kernelv1.GetItemRequest{Name: inc}))
	if err != nil {
		t.Fatalf("kim reads her incident: %v", err)
	}
	it := res.Msg.Item
	if _, ok := it.Fields.AsMap()["f_assignee"]; ok {
		t.Errorf("kim sees the assignee: %v", it.Fields.AsMap())
	}
	if it.Etag != "" {
		t.Errorf("kim gets etag %q; a customer gets none", it.Etag)
	}
	if it.Creator != "customer:kim" {
		t.Errorf("creator %q", it.Creator)
	}
	if staff := get(t, "sam-acme", inc); staff.Etag == "" || staff.Fields.AsMap()["f_assignee"] != "user:sam" {
		t.Errorf("sam: etag %q, fields %v; want both, as today", staff.Etag, staff.Fields.AsMap())
	}
}

// errBytes is everything a client gets of an error: code, message and
// details, as bytes.
func errBytes(t *testing.T, err error) []byte {
	t.Helper()
	ce, ok := err.(*connect.Error)
	if !ok {
		t.Fatalf("not a connect error: %v", err)
	}
	out := []byte(ce.Code().String() + "\x00" + ce.Message())
	for _, d := range ce.Details() {
		out = append(out, 0)
		out = append(out, d.Type()...)
		out = append(out, d.Bytes()...)
	}
	return out
}

func TestCustomer_API_HiddenAndMissing_TheSameBytes(t *testing.T) {
	desk := portal(t)
	inc := kimsIncident(t, desk)
	leeRes, err := customerCreates(t, asCustomer("lee", ""), incidentV2, desk, map[string]any{"f_title": "lee", "f_priority": "P3"})
	if err != nil {
		t.Fatalf("lee opens an incident: %v", err)
	}
	note := mkIn(t, "sam-acme", noteV1, inc, map[string]any{"f_body": "internal"})
	missing := "namespaces/acme/items/01a1fc38-0000-7000-8000-000000000000"
	kim := asCustomer("kim", "")
	for what, hidden := range map[string]string{"lee's incident": leeRes.Item.Name, "a note in her own": note} {
		_, e1 := kim.GetItem(testCtx(t), connect.NewRequest(&kernelv1.GetItemRequest{Name: hidden}))
		_, e2 := kim.GetItem(testCtx(t), connect.NewRequest(&kernelv1.GetItemRequest{Name: missing}))
		notFound(t, "GetItem "+what, e1)
		if string(errBytes(t, e1)) != string(errBytes(t, e2)) {
			t.Errorf("GetItem %s and a missing Item differ:\n%q\n%q", what, errBytes(t, e1), errBytes(t, e2))
		}
		_, e1 = kim.ListItems(testCtx(t), connect.NewRequest(&kernelv1.ListItemsRequest{Parent: hidden}))
		_, e2 = kim.ListItems(testCtx(t), connect.NewRequest(&kernelv1.ListItemsRequest{Parent: missing}))
		if string(errBytes(t, e1)) != string(errBytes(t, e2)) {
			t.Errorf("ListItems in %s and in a missing Item differ", what)
		}
		_, e1 = kim.ListItemEvents(testCtx(t), connect.NewRequest(&kernelv1.ListItemEventsRequest{Parent: hidden}))
		_, e2 = kim.ListItemEvents(testCtx(t), connect.NewRequest(&kernelv1.ListItemEventsRequest{Parent: missing}))
		if string(errBytes(t, e1)) != string(errBytes(t, e2)) {
			t.Errorf("ListItemEvents of %s and of a missing Item differ", what)
		}
		_, e1 = kim.CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{Parent: "namespaces/acme", RequestId: newID(),
			Type: replyV1, Container: hidden, Fields: fields(t, map[string]any{"f_body": "x"})}))
		_, e2 = kim.CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{Parent: "namespaces/acme", RequestId: newID(),
			Type: replyV1, Container: missing, Fields: fields(t, map[string]any{"f_body": "x"})}))
		if string(errBytes(t, e1)) != string(errBytes(t, e2)) {
			t.Errorf("a reply in %s and in a missing Item differ", what)
		}
	}
	// By key: a portal narrowed away from kim and a key nobody uses answer
	// the same search with the same bytes.
	hiddenDesk := mkIn(t, "hana", deskV2, "", map[string]any{"f_key": "DESK-12-" + word(), "f_name": "staff only"})
	key := get(t, "hana", hiddenDesk).Fields.AsMap()["f_key"]
	if _, err := as("hana").UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{Name: hiddenDesk, RequestId: newID(),
		Policy: &kernelv1.PolicyChange{Change: &kernelv1.PolicyChange_Narrow{Narrow: &kernelv1.Readers{Principals: []string{"user:hana"}}}}})); err != nil {
		t.Fatalf("narrow the desk: %v", err)
	}
	search := func(k any) *kernelv1.SearchItemsResponse {
		t.Helper()
		v, err := structpb.NewValue(k)
		if err != nil {
			t.Fatal(err)
		}
		res, err := kim.SearchItems(testCtx(t), connect.NewRequest(&kernelv1.SearchItemsRequest{Parent: "namespaces/acme",
			Filter: &kernelv1.ItemFilter{Types: []string{"namespaces/acme/types/desk"}, Fields: []*kernelv1.FieldFilter{{Field: "f_key", Eq: v}}}}))
		if err != nil {
			t.Fatalf("kim searches desks by key %v: %v", k, err)
		}
		return res.Msg
	}
	a, b := search(key), search("DESK-12-"+word())
	ab, _ := proto.Marshal(a)
	bb, _ := proto.Marshal(b)
	if len(a.Items) != 0 || string(ab) != string(bb) {
		t.Errorf("by key: a hidden desk %v, a missing key %v; want the same empty answer", a, b)
	}
}

func TestCustomer_API_ARefusalCarriesNoMetadata(t *testing.T) {
	desk := portal(t)
	inc := kimsIncident(t, desk)
	kim := asCustomer("kim", "")
	_, err := customerCreates(t, kim, noteV1, inc, map[string]any{"f_body": "x"})
	ce := apiError(t, "kim writes a note", err, connect.CodePermissionDenied, "NOT_ALLOWED")
	if info := errorInfo(t, ce); info != nil && len(info.Metadata) != 0 {
		t.Errorf("the refusal tells kim %v", info.Metadata)
	}
	_, err = kim.UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{Name: inc, RequestId: newID(),
		Fields: fields(t, map[string]any{"f_title": "x"})}))
	ce = apiError(t, "kim updates her incident", err, connect.CodePermissionDenied, "NOT_ALLOWED")
	if info := errorInfo(t, ce); info != nil && len(info.Metadata) != 0 {
		t.Errorf("the refusal tells kim %v", info.Metadata)
	}
	// Setting a field she may not: named, as for anyone (the Type is public).
	_, err = customerCreates(t, kim, incidentV2, desk, map[string]any{"f_title": "x", "f_priority": "P3", "f_assignee": "user:sam"})
	apiError(t, "kim assigns at creation", err, connect.CodePermissionDenied, "FIELD_NOT_WRITABLE")
}

func TestCreateItem_Requester(t *testing.T) {
	desk := portal(t)
	res, err := as("sam-acme").CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: incidentV2, Container: desk, Requester: "customer:kim",
		Fields: fields(t, map[string]any{"f_title": "phoned in " + word(), "f_priority": "P2"}),
	}))
	if err != nil {
		t.Fatalf("sam opens an incident for kim: %v", err)
	}
	if it := res.Msg.Item; it.Creator != "user:sam" || it.CreatorOnBehalfOf != "customer:kim" {
		t.Errorf("creator %s for %s; want user:sam for customer:kim", it.Creator, it.CreatorOnBehalfOf)
	}
	if _, err := asCustomer("kim", "").GetItem(testCtx(t), connect.NewRequest(&kernelv1.GetItemRequest{Name: res.Msg.Item.Name})); err != nil {
		t.Errorf("kim reads the incident opened for her: %v", err)
	}
	ask := func(c kernelv1connect.ItemServiceClient, container, requester string) error {
		_, err := c.CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
			Parent: "namespaces/acme", RequestId: newID(), Type: incidentV2, Container: container, Requester: requester,
			Fields: fields(t, map[string]any{"f_title": "x", "f_priority": "P3"}),
		}))
		return err
	}
	invalid(t, "a user as requester", ask(as("sam-acme"), desk, "user:ana"), "requester")
	invalid(t, "a customer names a requester", ask(asCustomer("kim", ""), desk, "customer:lee"), "requester")
	// Hidden from the portal, or never was: the same bytes.
	staffOnly := mkIn(t, "hana", deskV2, "", map[string]any{"f_key": "S", "f_name": "staff"})
	if _, err := as("hana").UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{Name: staffOnly, RequestId: newID(),
		Policy: &kernelv1.PolicyChange{Change: &kernelv1.PolicyChange_Narrow{Narrow: &kernelv1.Readers{Principals: []string{"user:hana", "user:sam"}}}}})); err != nil {
		t.Fatalf("narrow: %v", err)
	}
	e1, e2 := ask(as("sam-acme"), staffOnly, "customer:kim"), ask(as("sam-acme"), staffOnly, "customer:nobody")
	invalid(t, "kim, hidden from the portal", e1, "requester")
	if string(errBytes(t, e1)) != string(errBytes(t, e2)) {
		t.Errorf("hidden and nonexistent customers differ:\n%q\n%q", errBytes(t, e1), errBytes(t, e2))
	}
}

func TestCustomer_API_APortalBot_FixedPowers(t *testing.T) {
	desk := portal(t)
	inc := kimsIncident(t, desk)
	start := func(powers ...string) (string, error) {
		res, err := asCustomer("portal-bot", "").StartAgentSession(testCtx(t), connect.NewRequest(&kernelv1.StartAgentSessionRequest{
			Parent: "namespaces/acme", ActingFor: "customer:kim", Scopes: []string{"namespaces/acme"}, Powers: powers,
		}))
		if err != nil {
			return "", err
		}
		return res.Msg.Session.GetId(), nil
	}
	session, err := start("read", "create", "comment", "transition:*")
	if err != nil {
		t.Fatalf("the portal bot starts a session for kim: %v", err)
	}
	bot := asCustomer("portal-bot", session)
	if _, err := customerCreates(t, bot, replyV1, inc, map[string]any{"f_body": "from the portal"}); err != nil {
		t.Errorf("the bot replies for kim: %v", err)
	}
	got, err := bot.GetItem(testCtx(t), connect.NewRequest(&kernelv1.GetItemRequest{Name: inc}))
	if err != nil || got.Msg.Item.Etag != "" {
		t.Errorf("the bot reads kim's incident: %v, etag %q; want her view", err, got.Msg.GetItem().GetEtag())
	}
	_, err = start("read", "edit_fields")
	apiError(t, "a session for kim with edit_fields", err, connect.CodePermissionDenied, "NOT_ALLOWED")
}
