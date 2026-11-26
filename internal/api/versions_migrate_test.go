// Tests for track 1, item 6, step 2 (MigrateItem), over the wire.
//
//   - MigrateItem moves an open Item to the latest version of its Type.
//     The response holds the Item as the caller sees it, on the new
//     version, with item_seq and commit_seq.
//   - validate_only writes nothing, needs no request_id, and answers with
//     every problem (none: the Item fits). A problem is a code in
//     UPPER_SNAKE and what the Type declares, never a value.
//   - A migrate that does not fit is FAILED_PRECONDITION / NOT_MIGRATABLE,
//     with the checks in metadata (comma-separated, in order).
//   - Who may not is PERMISSION_DENIED / NOT_ALLOWED; an Item the caller
//     cannot see is NOT_FOUND; a missing or malformed name, type or
//     request_id is INVALID_ARGUMENT on that field.
//   - History shows one MIGRATED event: type is the new version,
//     previous_type the one the Item left.
//
// This file sorts after types_test.go on purpose: it registers Types of its
// own inside the test, after the test that lists every Type has run.
package api_test

import (
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const (
	apimigV1 = "namespaces/acme/types/apimig/versions/1"
	apimigV2 = "namespaces/acme/types/apimig/versions/2"
)

func apimigSrc(version, rest string) string {
	return "id: apimig\nversion: " + version + "\ninitial_state: draft\nfields:\n  - {id: f_title, type: string}\n" +
		"states:\n  - {id: draft, category: todo}\n  - {id: review, category: active}\n  - {id: done, category: done}\n" +
		"transitions:\n  - {from: draft, to: review}\n  - {from: review, to: done}\n" + rest
}

func registerAPIType(t *testing.T, src string) {
	t.Helper()
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := itemservice.New(pool).RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register %s v%d: %v", ty.ID, ty.Version, err)
	}
}

func TestMigrateItem(t *testing.T) {
	ctx := testCtx(t)
	registerAPIType(t, apimigSrc("1", ""))
	if _, err := pool.Exec(ctx, `insert into principal_roles (namespace_id, principal, role)
		values ('acme', 'user:bob', 'apimig_admin') on conflict do nothing`); err != nil {
		t.Fatalf("grant apimig_admin to bob: %v", err)
	}
	inReview := func() string {
		name := create(t, "ana", apimigV1, map[string]any{"f_title": "old " + word()}).Item.Name
		transition(t, "ana", name, "review")
		return name
	}
	a, b := inReview(), inReview()
	private := create(t, "ana", apimigV1, map[string]any{"f_title": "private " + word()}).Item.Name
	if _, err := as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: private, RequestId: newID(), Policy: &kernelv1.PolicyChange{Change: &kernelv1.PolicyChange_Narrow{Narrow: &kernelv1.Readers{Principals: []string{"user:ana"}}}},
	})); err != nil {
		t.Fatalf("ana narrows an Item to herself: %v", err)
	}
	registerAPIType(t, apimigSrc("2", "migrate:\n  allowed: ['role:apimig_admin']\n"))
	migrate := func(who string, req *kernelv1.MigrateItemRequest) (*kernelv1.MigrateItemResponse, error) {
		res, err := as(who).MigrateItem(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}

	// The request's own shape.
	_, err := migrate("bob", &kernelv1.MigrateItemRequest{Name: a, Type: apimigV2})
	invalid(t, "no request_id", err, "request_id")
	_, err = migrate("bob", &kernelv1.MigrateItemRequest{Name: a, RequestId: newID()})
	invalid(t, "no type", err, "type")
	_, err = migrate("bob", &kernelv1.MigrateItemRequest{Name: a, RequestId: newID(), Type: "namespaces/acme/types/apimig"})
	invalid(t, "a type without a version", err, "type")
	_, err = migrate("bob", &kernelv1.MigrateItemRequest{Name: "namespaces/acme/items/nope", RequestId: newID(), Type: apimigV2})
	invalid(t, "a malformed name", err, "name")

	// Who may, and what they see.
	_, err = migrate("ana", &kernelv1.MigrateItemRequest{Name: a, RequestId: newID(), Type: apimigV2})
	apiError(t, "ana, who holds no role the key names", err, connect.CodePermissionDenied, "NOT_ALLOWED")
	_, err = migrate("bob", &kernelv1.MigrateItemRequest{Name: private, RequestId: newID(), Type: apimigV2})
	notFound(t, "bob migrates an Item he cannot see", err)

	// The dry run: nothing written, no request_id, every problem.
	dry, err := migrate("bob", &kernelv1.MigrateItemRequest{Name: a, Type: apimigV2, ValidateOnly: true})
	if err != nil {
		t.Fatalf("the dry run: %v", err)
	}
	if len(dry.Problems) != 0 || dry.Item.GetType() != apimigV1 || dry.CommitSeq != 0 {
		t.Errorf("the dry run of an Item that fits: problems %v, the Item on %s, commit_seq %d; want none, still %s, 0",
			dry.Problems, dry.Item.GetType(), dry.CommitSeq, apimigV1)
	}
	dry, err = migrate("bob", &kernelv1.MigrateItemRequest{Name: a, Type: apimigV1, ValidateOnly: true})
	if err != nil {
		t.Fatalf("the dry run to the Item's own version: %v (a problem found is an answer, not an error)", err)
	}
	if len(dry.Problems) != 1 || dry.Problems[0].Check != "TARGET_NOT_LATEST" {
		t.Errorf("the dry run to the Item's own version: problems %v, want TARGET_NOT_LATEST", dry.Problems)
	}

	// The same for real: one reason, the checks in metadata.
	_, err = migrate("bob", &kernelv1.MigrateItemRequest{Name: a, RequestId: newID(), Type: apimigV1})
	ce := apiError(t, "a migrate to the Item's own version", err, connect.CodeFailedPrecondition, "NOT_MIGRATABLE")
	if ce != nil {
		if got := errorInfo(t, ce).GetMetadata()["checks"]; got != "TARGET_NOT_LATEST" {
			t.Errorf("NOT_MIGRATABLE metadata checks = %q, want TARGET_NOT_LATEST", got)
		}
	}

	// The migrate.
	before := get(t, "ana", a)
	req := &kernelv1.MigrateItemRequest{Name: a, RequestId: newID(), Type: apimigV2}
	res, err := migrate("bob", req)
	if err != nil {
		t.Fatalf("bob migrates the Item: %v", err)
	}
	if res.Item.GetType() != apimigV2 || res.Item.GetState() != "review" || res.CommitSeq == 0 || res.ItemSeq == 0 {
		t.Errorf("the answer: the Item on %s in %s, item_seq %d, commit_seq %d; want %s, review, both set",
			res.Item.GetType(), res.Item.GetState(), res.ItemSeq, res.CommitSeq, apimigV2)
	}
	after := get(t, "ana", a)
	if after.Type != apimigV2 {
		t.Errorf("ana reads the Item on %s, want %s", after.Type, apimigV2)
	}
	if after.Etag != before.Etag {
		t.Errorf("the etag went from %s to %s: a migrate changes nothing a caller could overwrite", before.Etag, after.Etag)
	}
	// A retry with the same request_id answers the same.
	again, err := migrate("bob", req)
	if err != nil || again.CommitSeq != res.CommitSeq {
		t.Errorf("the retry: commit_seq %d, %v; want %d", again.GetCommitSeq(), err, res.CommitSeq)
	}

	// History: one MIGRATED event, naming both versions.
	evs := events(t, "ana", a)
	last := evs[len(evs)-1]
	if last.Kind != kernelv1.ItemEventKind_ITEM_EVENT_KIND_MIGRATED || last.Type != apimigV2 || last.PreviousType != apimigV1 || last.Actor != "user:bob" {
		t.Errorf("the last event: kind %s, type %s, previous_type %s, by %s; want MIGRATED, %s, %s, user:bob",
			last.Kind, last.Type, last.PreviousType, last.Actor, apimigV2, apimigV1)
	}
	// The other Item is untouched.
	if got := get(t, "ana", b).Type; got != apimigV1 {
		t.Errorf("the Item nobody migrated is on %s, want %s", got, apimigV1)
	}
}
