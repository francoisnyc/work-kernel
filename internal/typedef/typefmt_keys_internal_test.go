package typedef

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typefmt"
)

// TestTypefmtKeys_AreTypedefs: pli fmt orders keys by typefmt.Keys, and
// typefmt does not import typedef, so this test holds the table to
// typedef's keys: at every struct of the format, Keys names exactly its
// yaml keys, both ways, and Keys has no path the format does not have.
// (The order is the JSON Schema's: typefmt's TestKeys_AreTheSchemasInItsOrder.)
func TestTypefmtKeys_AreTypedefs(t *testing.T) {
	visited := map[string]bool{}
	var walk func(rt reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		for rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		switch {
		case rt == writableByType, rt == olderVersionsType:
			return // a scalar or a list of strings: no keys
		case rt == acceptedType:
			rt = acceptedDocTyp // its map form; the list form has no keys
		}
		switch rt.Kind() {
		case reflect.Struct:
			if visited[path] {
				return
			}
			visited[path] = true
			want := yamlKeys(rt)
			got, ok := typefmt.Keys[path]
			if !ok {
				t.Errorf("typefmt.Keys lacks %q: want %v", path, sortedKeys(want))
				return
			}
			for _, k := range got {
				if _, ok := want[k]; !ok {
					t.Errorf("typefmt.Keys[%q] has %q, which typedef does not know", path, k)
				}
			}
			for _, k := range sortedKeys(want) {
				if !slices.Contains(got, k) {
					t.Errorf("typefmt.Keys[%q] lacks %q, which typedef knows", path, k)
				}
				walk(want[k].Type, path+"."+k)
			}
			if len(got) != len(slices.Compact(slices.Sorted(slices.Values(got)))) {
				t.Errorf("typefmt.Keys[%q] names a key twice: %v", path, got)
			}
		case reflect.Slice:
			walk(rt.Elem(), path+"[]")
		case reflect.Map:
			walk(rt.Elem(), path+"{}")
		}
	}
	walk(reflect.TypeFor[Type](), "")
	for _, path := range slices.Sorted(maps.Keys(typefmt.Keys)) {
		if !visited[path] {
			t.Errorf("typefmt.Keys has %q, which is no mapping of the Type format", path)
		}
	}
}
