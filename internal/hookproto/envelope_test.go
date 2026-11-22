// Tests for the hook protocol's envelopes (pli.hooks/v1): the JSON a Go
// value makes is the JSON the protocol documents, and within v1 the
// envelopes only grow. testdata/v1.0 holds the protocol as first released;
// those files are never edited.
package hookproto_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/hookproto"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "v1.0", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// jsonEqual compares two JSON documents by value.
func jsonEqual(t *testing.T, got, want []byte) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got is not JSON: %v", err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("want is not JSON: %v", err)
	}
	return reflect.DeepEqual(g, w)
}

// The check request the HR connector gets, built in Go, is the documented
// envelope field for field: names, nesting and omissions.
func TestRequest_IsTheDocumentedEnvelope(t *testing.T) {
	const item = "9d1e7c2a-0b4f-4e6d-8a3c-5f2e1d0c9b8a"
	req := hookproto.Request{
		Protocol: hookproto.Protocol, Kind: hookproto.KindCheck,
		RequestID: "6b1f4c1e-2d8a-4f0e-9a51-0c3b7d9e2f10", IdempotencyKey: "hold:3f2c9a1e-5b7d-4c8e-9f01-2a3b4c5d6e7f",
		Attempt: 2, Deadline: time.Date(2026, 11, 22, 10, 15, 5, 0, time.UTC),
		Hook:       hookproto.HookRef{Binding: "vacation_request@3#hr_check", Connector: "hr@1", Operation: "manager_of"},
		Event:      hookproto.Event{ID: item + ":4", Type: hookproto.EventTransitionRequested, Subject: "item/" + item},
		Actor:      hookproto.Actor{Principal: "agent:triage-bot", OnBehalfOf: "user:ana", AgentSession: "as_01"},
		Item:       hookproto.ItemRef{ID: item, Type: "vacation_request", TypeVersion: 3},
		Transition: &hookproto.Transition{From: "draft", To: "submitted"},
		Inputs:     map[string]any{"onBehalfOf": "user:ana", "createdBy": "agent:triage-bot"},
		Trace:      hookproto.Trace{CorrelationID: "corr-1", CausationID: item + ":4"},
		Connection: hookproto.ConnectionRef{Slot: "hr", Ref: "hr-directory"},
	}
	got, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if want := fixture(t, "request.check.json"); !jsonEqual(t, got, want) {
		t.Errorf("Request JSON =\n%s\nwant\n%s", got, want)
	}

	// An enrich request has no transition, and no deadline when the caller
	// set none: both are left out, not sent empty.
	req.Kind, req.Transition, req.Deadline = hookproto.KindEnrich, nil, time.Time{}
	got, _ = json.Marshal(req)
	var m map[string]any
	_ = json.Unmarshal(got, &m)
	if _, ok := m["transition"]; ok {
		t.Errorf("enrich request has a transition: %s", got)
	}
	if _, ok := m["deadline"]; ok {
		t.Errorf("request without a deadline sends one: %s", got)
	}
}

// leaves flattens a JSON value to path -> leaf value.
func leaves(prefix string, v any, out map[string]any) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			leaves(prefix+"/"+k, e, out)
		}
	case []any:
		for i, e := range x {
			leaves(prefix+"/"+strconv.Itoa(i), e, out)
		}
	default:
		out[prefix] = v
	}
}

// Every frozen fixture survives a round trip through the current Go types
// with every field and value it had: v1 only grows.
func TestV1Fixtures_OnlyGrow(t *testing.T) {
	types := map[string]func() any{
		"request.check.json":    func() any { return &hookproto.Request{} },
		"request.enrich.json":   func() any { return &hookproto.Request{} },
		"response.correct.json": func() any { return &hookproto.Response{} },
		"response.deny.json":    func() any { return &hookproto.Response{} },
		"response.enrich.json":  func() any { return &hookproto.Response{} },
		"problem.json":          func() any { return &hookproto.Problem{} },
		"descriptor.hr.json":    func() any { return &hookproto.Descriptor{} },
	}
	files, err := filepath.Glob(filepath.Join("testdata", "v1.0", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		name := filepath.Base(f)
		t.Run(name, func(t *testing.T) {
			newValue, ok := types[name]
			if !ok {
				t.Fatalf("no Go type for fixture %s: add it to this test", name)
			}
			data := fixture(t, name)
			v := newValue()
			if err := json.Unmarshal(data, v); err != nil {
				t.Fatalf("decode: %v", err)
			}
			back, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			var was, now any
			_ = json.Unmarshal(data, &was)
			_ = json.Unmarshal(back, &now)
			before, after := map[string]any{}, map[string]any{}
			leaves("", was, before)
			leaves("", now, after)
			for path, value := range before {
				if got, ok := after[path]; !ok || !reflect.DeepEqual(got, value) {
					t.Errorf("%s: %s was %v, is now %v (present: %v)", name, path, value, got, ok)
				}
			}
		})
	}
	if len(files) < len(types) {
		t.Errorf("found %d fixtures, want at least %d", len(files), len(types))
	}
}

// Both sides ignore fields they do not know: a v1.1 sender's additions do
// not break a v1.0 reader.
func TestEnvelopes_UnknownFieldsAreIgnored(t *testing.T) {
	var req hookproto.Request
	body := []byte(`{"protocol":"pli.hooks/v1","kind":"check","futureField":1,"hook":{"binding":"x@1#h","later":true}}`)
	if err := json.Unmarshal(body, &req); err != nil || req.Hook.Binding != "x@1#h" {
		t.Errorf("decode with unknown fields = %+v, %v", req, err)
	}
}

// JSON Schema's type is a string or a list; a descriptor may use either,
// and v1 writes the list.
func TestTypeList_ReadsBothForms(t *testing.T) {
	var p struct {
		A hookproto.Property `json:"a"`
		B hookproto.Property `json:"b"`
	}
	if err := json.Unmarshal([]byte(`{"a":{"type":"string"},"b":{"type":["string","null"]}}`), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !slices.Equal(p.A.Type, []string{"string"}) || !slices.Equal(p.B.Type, []string{"string", "null"}) {
		t.Errorf("types = %v, %v; want [string], [string null]", p.A.Type, p.B.Type)
	}
	out, _ := json.Marshal(p.A)
	if string(out) != `{"type":["string"]}` {
		t.Errorf("Property JSON = %s, want the list form", out)
	}
	if err := json.Unmarshal([]byte(`{"a":{"type":3}}`), &p); err == nil {
		t.Error("a numeric type was accepted")
	}
}

func TestMajor(t *testing.T) {
	for v, want := range map[string]int{"1.4.0": 1, "1.5.12": 1, "2.0.0": 2, "10.1.0-rc.1": 10, "1.0.0+build.7": 1} {
		if got, err := hookproto.Major(v); err != nil || got != want {
			t.Errorf("Major(%q) = %d, %v; want %d", v, got, err, want)
		}
	}
	for _, v := range []string{"", "1", "1.4", "v1.4.0", "one.4.0", "-1.0.0"} {
		if _, err := hookproto.Major(v); err == nil {
			t.Errorf("Major(%q) accepted, want an error: not semver", v)
		}
	}
}

func TestDescriptor_Operation(t *testing.T) {
	var d hookproto.Descriptor
	if err := json.Unmarshal(fixture(t, "descriptor.hr.json"), &d); err != nil {
		t.Fatal(err)
	}
	op, ok := d.Operation("manager_of")
	if !ok || !slices.Equal(op.Kinds, []hookproto.Kind{hookproto.KindCheck}) || len(op.InputSchema.Properties) != 2 {
		t.Errorf("Operation(manager_of) = %+v, %v", op, ok)
	}
	if _, ok := d.Operation("nope"); ok {
		t.Error("Operation(nope) found")
	}
}
