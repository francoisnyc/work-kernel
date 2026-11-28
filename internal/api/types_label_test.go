// The Type's own label and help over the wire (the Inbox, the look; the
// owner's decisions of 2026-11-28): kernel.v1's Type grows by two additive
// fields, label (21) and help (22). Empty means none. GetType and ListTypes
// carry them, for the versions of types/ that declare them.
package api_test

import (
	"fmt"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

// The versions of types/ with a label of their own, and that label.
var typeLabels = map[string]string{
	"approval/versions/3":          "Approval",
	"access_request/versions/4":    "Access request",
	"equipment_request/versions/3": "Equipment request",
	"incident/versions/9":          "Incident",
}

func TestListTypes_TheTypesOwnLabel(t *testing.T) {
	res, err := as("bea").ListTypes(testCtx(t), connect.NewRequest(&kernelv1.ListTypesRequest{
		Parent: "namespaces/globex",
	}))
	if err != nil {
		t.Fatalf("ListTypes: %v", err)
	}
	seen := 0
	for _, ty := range res.Msg.Types {
		short := fmt.Sprintf("%s/versions/%d", ty.TypeId, ty.Version)
		want, labelled := typeLabels[short]
		if labelled {
			seen++
		}
		if ty.Label != want || ty.Help != "" {
			t.Errorf("%s: label, help = %q, %q, want %q and no help", ty.Name, ty.Label, ty.Help, want)
		}
	}
	if seen != len(typeLabels) {
		t.Errorf("ListTypes listed %d of the %d labelled versions", seen, len(typeLabels))
	}
}

func TestGetType_TheTypesOwnLabel(t *testing.T) {
	for short, want := range typeLabels {
		res, err := as("bea").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{
			Name: "namespaces/globex/types/" + short,
		}))
		if err != nil {
			t.Errorf("GetType %s: %v", short, err)
			continue
		}
		if got := res.Msg.Type.GetLabel(); got != want {
			t.Errorf("GetType %s: label = %q, want %q", short, got, want)
		}
	}
	// A version without one sends it empty.
	res, err := as("bea").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{
		Name: "namespaces/globex/types/approval/versions/2",
	}))
	if err != nil {
		t.Fatalf("GetType approval@2: %v", err)
	}
	if res.Msg.Type.GetLabel() != "" || res.Msg.Type.GetHelp() != "" {
		t.Errorf("approval@2: label, help = %q, %q, want none", res.Msg.Type.GetLabel(), res.Msg.Type.GetHelp())
	}
}
