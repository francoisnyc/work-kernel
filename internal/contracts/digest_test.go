// Tests for Ventoux V1: a contract's digest, sha256 of its machine contract.
// The machine contract is everything a Type or a connector relies on: the
// names, the inputs and outputs with their types, format, enum and trust,
// the kinds, sideEffects, async, errors and risk. The descriptions are not
// in it: they are required, but stay editable (their changes show in the
// pull request's diff). The digest is what contracts.lock pins and what
// ListActions serves.
package contracts_test

import (
	"regexp"
	"testing"
)

func TestDigest_Format(t *testing.T) {
	d := mustParse(t, validFile, []byte(validJSON)).Digest()
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(d) {
		t.Errorf("digest = %q, want sha256:<64 hex>", d)
	}
}

// Canonical: neither spacing nor key order changes it, and an input without
// x-pli-trust is the same as one with "any".
func TestDigest_Canonical(t *testing.T) {
	base := mustParse(t, validFile, []byte(validJSON)).Digest()
	same := map[string][]byte{
		// json.MarshalIndent writes object keys sorted, unlike validJSON.
		"keys reordered, respaced": changed(t, func(map[string]any) {}),
		"trust any written out": changed(t, func(m map[string]any) {
			prop(m, "inputSchema", "owner")["x-pli-trust"] = "any"
		}),
	}
	for what, data := range same {
		if d := mustParse(t, validFile, data).Digest(); d != base {
			t.Errorf("%s: digest %s, want %s", what, d, base)
		}
	}
}

// The descriptions are not in the digest.
func TestDigest_LeavesOutDescriptions(t *testing.T) {
	base := mustParse(t, validFile, []byte(validJSON)).Digest()
	edits := map[string]func(m map[string]any){
		"the operation's": func(m map[string]any) { m["description"] = "Finds the tier of a customer." },
		"an input's":      func(m map[string]any) { prop(m, "inputSchema", "owner")["description"] = "The account's owner." },
		"an output's":     func(m map[string]any) { prop(m, "outputSchema", "tier")["description"] = "Gold or silver." },
	}
	for what, edit := range edits {
		if d := mustParse(t, validFile, changed(t, edit)).Digest(); d != base {
			t.Errorf("editing %s description changed the digest", what)
		}
	}
}

// Every part of the machine contract is in the digest.
func TestDigest_CoversTheMachineContract(t *testing.T) {
	base := mustParse(t, validFile, []byte(validJSON)).Digest()
	cases := []struct {
		name, file string
		change     func(m map[string]any)
	}{
		{"the connector", "crm.lookup@1.json", func(m map[string]any) { m["connector"] = "crm" }},
		{"the operation", "acme.find@1.json", func(m map[string]any) { m["name"] = "find" }},
		{"the major", "acme.lookup@2.json", func(m map[string]any) { m["major"] = 2 }},
		{"the kinds", "", func(m map[string]any) { m["kinds"] = []any{"check", "enrich"} }},
		{"an input's type", "", func(m map[string]any) { prop(m, "inputSchema", "customerId")["type"] = []any{"integer", "null"} }},
		{"an input's nullability", "", func(m map[string]any) { prop(m, "inputSchema", "customerId")["type"] = "string" }},
		{"an input's format", "", func(m map[string]any) { prop(m, "inputSchema", "owner")["format"] = "date" }},
		{"an input's trust", "", func(m map[string]any) { prop(m, "inputSchema", "customerId")["x-pli-trust"] = "any" }},
		{"the inputs' order", "", func(m map[string]any) { m["inputs"] = []any{"owner", "customerId"} }},
		{"an output's enum", "", func(m map[string]any) { prop(m, "outputSchema", "tier")["enum"] = []any{"gold", "silver", "bronze"} }},
		{"an output's name", "", func(m map[string]any) {
			props := m["outputSchema"].(map[string]any)["properties"].(map[string]any)
			props["level"] = props["tier"]
			delete(props, "tier")
			m["outputs"] = []any{"level"}
		}},
		{"the reasons", "", func(m map[string]any) { m["errors"] = []any{"unknown_customer", "closed_account"} }},
		{"the risk", "", func(m map[string]any) { m["risk"] = "medium" }},
		{"the side effects", "", func(m map[string]any) {
			m["kinds"] = []any{"action"}
			m["sideEffects"] = "some"
			delete(m, "errors")
		}},
	}
	seen := map[string]string{base: "the valid contract"}
	for _, c := range cases {
		file := c.file
		if file == "" {
			file = validFile
		}
		d := mustParse(t, file, changed(t, c.change)).Digest()
		if d == base {
			t.Errorf("changing %s left the digest as it was", c.name)
		}
		if other, dup := seen[d]; dup && d != base {
			t.Errorf("changing %s gives the same digest as %s", c.name, other)
		}
		seen[d] = c.name
	}
}
