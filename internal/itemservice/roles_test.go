// Tests for Step 0, part 3: EnsureRoles, which the item service calls at
// start in dev-token mode with the roles the token file names. It only
// adds: a role missing from the database is added and reported; one the
// file no longer names is reported and kept; namespaces the call does not
// name are not looked at.
package itemservice_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func roleRows(t *testing.T, ns string) []itemservice.Role {
	t.Helper()
	rows, err := pool.Query(testCtx(t),
		`select namespace_id, principal, role from principal_roles where namespace_id = $1 order by principal, role`, ns)
	if err != nil {
		t.Fatalf("read roles: %v", err)
	}
	defer rows.Close()
	var out []itemservice.Role
	for rows.Next() {
		var r itemservice.Role
		if err := rows.Scan(&r.Namespace, &r.Principal, &r.Role); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func TestEnsureRoles_AddsOnlyWhatIsMissing(t *testing.T) {
	k := itemservice.New(pool)
	ns, other := newNamespace(t, k), newNamespace(t, k)
	bob := itemservice.Role{Namespace: ns, Principal: "user:bob", Role: "approver"}
	ana := itemservice.Role{Namespace: ns, Principal: "user:ana", Role: "auditor"}

	res, err := k.EnsureRoles(testCtx(t), []string{ns, other}, []itemservice.Role{bob, ana})
	if err != nil {
		t.Fatalf("EnsureRoles: %v", err)
	}
	if want := []itemservice.Role{ana, bob}; !slices.Equal(sortedRoles(res.Added), want) || len(res.Unlisted) != 0 {
		t.Errorf("first call = %+v, want added %v and nothing unlisted", res, want)
	}
	if got := roleRows(t, ns); !slices.Equal(got, []itemservice.Role{ana, bob}) {
		t.Errorf("rows = %v, want ana and bob", got)
	}

	// Again: nothing to add.
	res, err = k.EnsureRoles(testCtx(t), []string{ns, other}, []itemservice.Role{bob, ana})
	if err != nil || len(res.Added) != 0 || len(res.Unlisted) != 0 {
		t.Errorf("second call = %+v, %v; want nothing added or unlisted", res, err)
	}
}

// A role in the database that the list no longer names is reported and
// kept. Roles in a namespace the call does not name are left alone and not
// reported.
func TestEnsureRoles_KeepsAndReportsUnlistedRoles(t *testing.T) {
	k := itemservice.New(pool)
	ns, elsewhere := newNamespace(t, k), newNamespace(t, k)
	old := itemservice.Role{Namespace: ns, Principal: "user:carol", Role: "approver"}
	far := itemservice.Role{Namespace: elsewhere, Principal: "user:dan", Role: "approver"}
	for _, r := range []itemservice.Role{old, far} {
		if _, err := pool.Exec(testCtx(t),
			`insert into principal_roles (namespace_id, principal, role) values ($1, $2, $3)`,
			r.Namespace, r.Principal, r.Role); err != nil {
			t.Fatal(err)
		}
	}
	bob := itemservice.Role{Namespace: ns, Principal: "user:bob", Role: "approver"}
	res, err := k.EnsureRoles(testCtx(t), []string{ns}, []itemservice.Role{bob})
	if err != nil {
		t.Fatalf("EnsureRoles: %v", err)
	}
	if !slices.Equal(res.Added, []itemservice.Role{bob}) || !slices.Equal(res.Unlisted, []itemservice.Role{old}) {
		t.Errorf("EnsureRoles = %+v, want added [bob], unlisted [carol]", res)
	}
	if got := roleRows(t, ns); !slices.Equal(got, []itemservice.Role{bob, old}) {
		t.Errorf("rows in %s = %v, want bob and carol (kept)", ns, got)
	}
	if got := roleRows(t, elsewhere); !slices.Equal(got, []itemservice.Role{far}) {
		t.Errorf("rows in %s = %v, want dan, untouched", elsewhere, got)
	}
}

// A role is a bare name for a user: or agent: principal, in a valid
// namespace that the call names. Anything else is ErrInvalid and adds
// nothing, not even the good entries.
func TestEnsureRoles_RefusesBadRoles(t *testing.T) {
	k := itemservice.New(pool)
	ns := newNamespace(t, k)
	good := itemservice.Role{Namespace: ns, Principal: "user:bob", Role: "approver"}
	for name, bad := range map[string]itemservice.Role{
		"prefixed role":       {Namespace: ns, Principal: "user:bob", Role: "role:approver"},
		"empty role":          {Namespace: ns, Principal: "user:bob", Role: ""},
		"untyped principal":   {Namespace: ns, Principal: "bob", Role: "approver"},
		"kernel principal":    {Namespace: ns, Principal: "system:timer", Role: "approver"},
		"bad namespace":       {Namespace: "Not Valid", Principal: "user:bob", Role: "approver"},
		"namespace not named": {Namespace: "ns-unnamed", Principal: "user:bob", Role: "approver"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := k.EnsureRoles(testCtx(t), []string{ns}, []itemservice.Role{good, bad})
			if !errors.Is(err, itemservice.ErrInvalid) {
				t.Errorf("EnsureRoles(%+v) = %v, want ErrInvalid", bad, err)
			}
			if got := roleRows(t, ns); len(got) != 0 {
				t.Errorf("rows = %v, want none", got)
			}
		})
	}
}

func sortedRoles(rs []itemservice.Role) []itemservice.Role {
	out := slices.Clone(rs)
	slices.SortFunc(out, func(a, b itemservice.Role) int {
		if a.Principal != b.Principal {
			if a.Principal < b.Principal {
				return -1
			}
			return 1
		}
		if a.Role < b.Role {
			return -1
		}
		if a.Role > b.Role {
			return 1
		}
		return 0
	})
	return out
}
