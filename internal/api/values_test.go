// Tests for Step 0, part 1: field values are plain JSON on the wire,
// converted and checked against the field's type in the Item's Type. Step 0
// has the existing types only: string, int, bool, enum. An int is a string
// on the way out (int64 does not fit a JSON number); on the way in a number
// or a numeric string is accepted.
package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

// post sends a Connect-protocol JSON request as curl would, and returns the
// HTTP status and the decoded body.
func post(t *testing.T, token, method, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/kernel.v1.ItemService/"+method, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return res.StatusCode, out
}

// The contract as plain JSON: what pli, curl or a browser sends and gets.
func TestValues_PlainJSONOnTheWire(t *testing.T) {
	status, body := post(t, "tok-ana", "CreateItem", `{
		"parent": "namespaces/acme",
		"requestId": "`+newID()+`",
		"type": "namespaces/acme/types/ticket/versions/1",
		"fields": {"f_title": "Printer", "f_priority": 3, "f_urgent": true, "f_resolution": "fixed"}
	}`)
	if status != http.StatusOK {
		t.Fatalf("HTTP %d: %v", status, body)
	}
	it := body["item"].(map[string]any)
	f := it["fields"].(map[string]any)
	if f["f_title"] != "Printer" || f["f_priority"] != "3" || f["f_urgent"] != true || f["f_resolution"] != "fixed" {
		t.Errorf("fields = %v; want the int as the string \"3\", the rest as sent", f)
	}
	// int64s are strings in protobuf's JSON mapping, as AIP and connect expect.
	if body["itemSeq"] != "1" {
		t.Errorf("itemSeq = %#v, want \"1\"", body["itemSeq"])
	}
	if _, ok := body["commitSeq"].(string); !ok {
		t.Errorf("commitSeq = %#v, want a string", body["commitSeq"])
	}

	// A numeric string is accepted for an int, and JSON null clears a field.
	status, body = post(t, "tok-ana", "UpdateItem", `{
		"name": "`+it["name"].(string)+`", "requestId": "`+newID()+`",
		"fields": {"f_priority": "-7", "f_resolution": null}
	}`)
	if status != http.StatusOK {
		t.Fatalf("UpdateItem: HTTP %d: %v", status, body)
	}
	f = body["item"].(map[string]any)["fields"].(map[string]any)
	if f["f_priority"] != "-7" {
		t.Errorf("f_priority = %#v, want \"-7\"", f["f_priority"])
	}
	if _, ok := f["f_resolution"]; ok {
		t.Errorf("f_resolution = %#v, want it cleared (absent)", f["f_resolution"])
	}
}

// A value that does not fit its field is INVALID_ARGUMENT naming the field.
func TestValues_MismatchNamesTheField(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]any
		field  string
	}{
		{"unknown field", map[string]any{"f_ghost": "x"}, "fields.f_ghost"},
		{"number for a string", map[string]any{"f_title": 42}, "fields.f_title"},
		{"fraction for an int", map[string]any{"f_priority": 1.5}, "fields.f_priority"},
		{"word for an int", map[string]any{"f_priority": "seven"}, "fields.f_priority"},
		{"numeric string with a fraction", map[string]any{"f_priority": "1.5"}, "fields.f_priority"},
		// The kernel keeps ints within what a JSON number holds exactly.
		{"int beyond 2^53-1", map[string]any{"f_priority": "9007199254740993"}, "fields.f_priority"},
		{"int beyond int64", map[string]any{"f_priority": "99999999999999999999"}, "fields.f_priority"},
		{"bool for an int", map[string]any{"f_priority": true}, "fields.f_priority"},
		{"string for a bool", map[string]any{"f_urgent": "true"}, "fields.f_urgent"},
		{"name outside the enum", map[string]any{"f_resolution": "magic"}, "fields.f_resolution"},
		{"list for a string", map[string]any{"f_title": []any{"a"}}, "fields.f_title"},
		{"object for a string", map[string]any{"f_title": map[string]any{"a": "b"}}, "fields.f_title"},
		{"null on create", map[string]any{"f_title": nil}, "fields.f_title"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := as("ana").CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
				Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1, Fields: fields(t, c.fields),
			}))
			invalid(t, "CreateItem", err, c.field)
		})
	}

	// The same check on an update.
	name := create(t, "ana", ticketV1, nil).Item.Name
	_, err := as("ana").UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: name, RequestId: newID(), Fields: fields(t, map[string]any{"f_title": "ok", "f_priority": "high"}),
	}))
	invalid(t, "UpdateItem", err, "fields.f_priority")
}
