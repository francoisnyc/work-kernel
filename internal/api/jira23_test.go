// Tests for Jira part 2, step 3a, through the public API: not_empty in
// filters, for SearchItems and ListItems. (The tests that need test Types
// are in package apistep3: Types are global, and this package's tests list
// exactly the repository's.)
package api_test

import (
	"slices"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

// not_empty: "has a value". Stories with a label, among those with none.
func TestNotEmpty_API(t *testing.T) {
	plat := space(t)
	summary := "Tagged " + word()
	mk3 := func(f map[string]any) string {
		t.Helper()
		f["f_summary"] = summary
		res, err := as("ana").CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
			Parent: "namespaces/acme", RequestId: newID(), Type: platV3("story"), Container: plat, Fields: fields(t, f),
		}))
		if err != nil {
			t.Fatalf("CreateItem story@3: %v", err)
		}
		return res.Msg.Item.Name
	}
	labelled := mk3(map[string]any{"f_labels": []any{"sso"}})
	pointed := mk3(map[string]any{"f_points": 3})
	bare := mk3(map[string]any{})

	search := func(ffs ...*kernelv1.FieldFilter) ([]string, error) {
		res, err := as("ana").SearchItems(testCtx(t), connect.NewRequest(&kernelv1.SearchItemsRequest{
			Parent: "namespaces/acme", Query: summary,
			Filter: &kernelv1.ItemFilter{Types: []string{"namespaces/acme/types/story"}, Fields: ffs},
		}))
		if err != nil {
			return nil, err
		}
		var names []string
		for _, it := range res.Msg.Items {
			names = append(names, it.Name)
		}
		slices.Sort(names)
		return names, nil
	}
	sorted := func(names ...string) []string { slices.Sort(names); return names }

	if got, err := search(&kernelv1.FieldFilter{Field: "f_labels", NotEmpty: true}); err != nil || !slices.Equal(got, sorted(labelled)) {
		t.Errorf("not_empty on a set = %v, %v; want the labelled story", got, err)
	}
	if got, err := search(&kernelv1.FieldFilter{Field: "f_points", NotEmpty: true}); err != nil || !slices.Equal(got, sorted(pointed)) {
		t.Errorf("not_empty on a single value = %v, %v; want the story with points", got, err)
	}
	if got, err := search(&kernelv1.FieldFilter{Field: "f_labels", Empty: true}, &kernelv1.FieldFilter{Field: "f_points", Empty: true}); err != nil || !slices.Equal(got, sorted(bare)) {
		t.Errorf("empty twice = %v, %v; want the bare story", got, err)
	}
	listed, err := as("ana").ListItems(testCtx(t), connect.NewRequest(&kernelv1.ListItemsRequest{
		Parent: plat, Filter: &kernelv1.ItemFilter{Fields: []*kernelv1.FieldFilter{{Field: "f_labels", NotEmpty: true}}},
	}))
	if err != nil {
		t.Errorf("ListItems with not_empty: %v", err)
	} else if len(listed.Msg.Items) != 1 || listed.Msg.Items[0].Name != labelled {
		t.Errorf("ListItems with not_empty = %v; want the labelled story", listed.Msg.Items)
	}
	for name, ff := range map[string]*kernelv1.FieldFilter{
		"not_empty with empty":   {Field: "f_labels", Empty: true, NotEmpty: true},
		"not_empty with a bound": {Field: "f_points", NotEmpty: true, Gt: fields(t, map[string]any{"v": 1}).Fields["v"]},
		"not_empty with eq":      {Field: "f_labels", NotEmpty: true, Eq: fields(t, map[string]any{"v": "sso"}).Fields["v"]},
	} {
		_, err := search(ff)
		malformed(t, name, err)
	}
}
