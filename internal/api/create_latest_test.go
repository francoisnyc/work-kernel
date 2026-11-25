// Tests for track 1, item 6, step 1 (a create names the latest version),
// over the wire, on the repository's Types.
//
// build_step has two versions and its latest, build_step@2, keeps no older
// version open: a create of build_step@1 is refused INVALID_ARGUMENT /
// INVALID_REQUEST with a field violation on type, the answer a version
// that is not registered gets (no new reason, no contract change). The
// violation's text is fixed and names no Type, version or Item.
//
// ticket@8 keeps every older ticket open (start.older_versions: all), so a
// ticket@1 is created as before and stays a ticket@1.
package api_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

const buildStepV1 = "namespaces/acme/types/build_step/versions/1"

// typeViolation is the description of the field violation on type.
func typeViolation(t *testing.T, err error) string {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a connect error", err)
	}
	for _, d := range ce.Details() {
		v, derr := d.Value()
		if derr != nil {
			t.Fatalf("decode error detail: %v", derr)
		}
		if br, ok := v.(*errdetails.BadRequest); ok {
			for _, fv := range br.FieldViolations {
				if fv.Field == "type" {
					return fv.Description
				}
			}
		}
	}
	return ""
}

func TestCreateLatest_AnOlderVersionIsRefusedOnType(t *testing.T) {
	ctx := testCtx(t)
	open := func(who, typ string) error {
		_, err := as(who).CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
			Parent: "namespaces/acme", RequestId: newID(), Type: typ,
			Fields: fields(t, map[string]any{"f_title": "Step " + word()}),
		}))
		return err
	}
	err := open("ana", buildStepV1)
	invalid(t, "ana creates build_step@1, with build_step@2 registered", err, "type")
	text := typeViolation(t, err)
	if !strings.Contains(text, "latest version") {
		t.Errorf("the violation on type reads %q; want it to say to create the latest version", text)
	}
	// Fixed text: no Type ID, no version number, no name.
	if regexp.MustCompile(`build_step|namespaces/|[0-9]`).MatchString(text) {
		t.Errorf("the violation on type reads %q; want a fixed text with no Type, version or name", text)
	}
	// The same bytes for another caller and another closed version.
	again := open("bob", buildStepV1)
	invalid(t, "bob creates build_step@1", again, "type")
	if got := typeViolation(t, again); got != text {
		t.Errorf("the violation reads %q for bob and %q for ana; want one fixed text", got, text)
	}

	// The latest version is created as before.
	newStep(t) // fails the test if build_step@2 is refused
}

// ticket@8 keeps the older tickets open: a ticket@1 is created and stays
// one.
func TestCreateLatest_AVersionTheLatestKeepsOpen(t *testing.T) {
	got := create(t, "ana", ticketV1, nil).Item
	if got.Type != ticketV1 {
		t.Errorf("a ticket created as %s is a %s", ticketV1, got.Type)
	}
	if _, err := as("ana").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{
		Name: "namespaces/acme/types/ticket/versions/8"})); err != nil {
		t.Errorf("ticket@8 is not loaded: %v", err)
	}
}
