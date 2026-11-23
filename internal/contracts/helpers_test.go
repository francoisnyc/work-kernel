package contracts_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
)

// validFile is the name validJSON must have.
const validFile = "acme.lookup@1.json"

// validJSON is a valid contract that uses every part of the format: a
// check with a nullable string input, a principal input with no trust
// (any), an enum output and a reason. Tests change it one way at a time.
const validJSON = `{
  "connector": "acme",
  "major": 1,
  "name": "lookup",
  "description": "Looks up a customer's tier.",
  "kinds": ["check"],
  "inputs": ["customerId", "owner"],
  "inputSchema": {"type": "object", "properties": {
    "customerId": {"type": ["string", "null"], "description": "The customer's ID.", "x-pli-trust": "trusted"},
    "owner": {"type": "string", "format": "pli-principal", "description": "Who owns the account."}
  }},
  "outputs": ["tier"],
  "outputSchema": {"type": "object", "properties": {
    "tier": {"type": "string", "enum": ["gold", "silver"], "description": "The customer's tier."}
  }},
  "sideEffects": "none",
  "async": "forbidden",
  "errors": ["unknown_customer"],
  "risk": "low"
}`

// object reads validJSON as a map a case can change.
func object(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(validJSON), &m); err != nil {
		t.Fatalf("test bug: validJSON: %v", err)
	}
	return m
}

// changed returns validJSON with change applied, as JSON.
func changed(t *testing.T, change func(m map[string]any)) []byte {
	t.Helper()
	m := object(t)
	change(m)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// prop returns a property of the input (in) or output schema.
func prop(m map[string]any, schema, name string) map[string]any {
	return m[schema].(map[string]any)["properties"].(map[string]any)[name].(map[string]any)
}

func mustParse(t *testing.T, file string, data []byte) contracts.Contract {
	t.Helper()
	c, err := contracts.Parse(file, data)
	if err != nil {
		t.Fatalf("Parse(%s): %v", file, err)
	}
	return c
}

// wantError fails unless err is set and its message contains every want.
func wantError(t *testing.T, what string, err error, want ...string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: no error", what)
		return
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("%s: error %q does not name %q", what, err, w)
		}
	}
}
