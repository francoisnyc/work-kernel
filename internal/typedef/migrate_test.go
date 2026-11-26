// Tests for track 1, item 6, step 2: the Type key migrate.allowed and its
// load checks.
//
//   - migrate: {allowed: [role:<name>, principal:user:<id>]}: who may
//     migrate an open Item of an older version to this one, while it is the
//     latest. Absent: nobody. The item service applies it
//     (internal/itemservice, migrate_test.go).
//   - Stage 2, the Type alone: a block that says nothing (REQUIRED); a list
//     that names nobody, and an entry that is not a role or a user:
//     creator, field:, nobody, container_creator, people, agents, an agent,
//     a customer, the timer, a role with no name (INVALID_VALUE); an entry
//     twice (DUPLICATE, on the second); a key the block does not have
//     (UNKNOWN_KEY).
//   - The stored form keeps the block, and a Type without one stores no
//     key for it.
package typedef_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func TestMigrateKey_Parses(t *testing.T) {
	ty, err := typedef.Parse([]byte(stAsk(2, "migrate:\n  allowed: ['role:ask_admin', 'principal:user:ana']\n")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ty.Migrate == nil || !slices.Equal(ty.Migrate.Allowed, []string{"role:ask_admin", "principal:user:ana"}) {
		t.Errorf("Migrate = %+v, want allowed [role:ask_admin principal:user:ana]", ty.Migrate)
	}
	// On a first version too: there is no older Item yet, and the key is
	// still a valid thing to say.
	if _, err := typedef.Parse([]byte(stAsk(1, "migrate:\n  allowed: ['role:ask_admin']\n"))); err != nil {
		t.Errorf("the key on a first version: %v", err)
	}
	plain, err := typedef.Parse([]byte(stAsk(2, "")))
	if err != nil || plain.Migrate != nil {
		t.Errorf("a Type without the block: Migrate = %+v, %v; want nil", plain.Migrate, err)
	}
}

func TestMigrateKey_TheTypeAlone(t *testing.T) {
	cases := []struct {
		name, block string
		code        typedef.Code
		path        string
	}{
		{"a block that says nothing", "migrate: {}\n", typedef.CodeRequired, "migrate"},
		{"a list that names nobody", "migrate:\n  allowed: []\n", typedef.CodeInvalidValue, "migrate.allowed"},
		{"creator: an administrator's act, not the requester's", "migrate:\n  allowed: [creator]\n", typedef.CodeInvalidValue, "migrate.allowed[0]"},
		{"a field of the Item", "migrate:\n  allowed: ['field:f_title']\n", typedef.CodeInvalidValue, "migrate.allowed[0]"},
		{"nobody: leave the key out", "migrate:\n  allowed: [nobody]\n", typedef.CodeInvalidValue, "migrate.allowed[0]"},
		{"container_creator", "migrate:\n  allowed: [container_creator]\n", typedef.CodeInvalidValue, "migrate.allowed[0]"},
		{"people", "migrate:\n  allowed: [people]\n", typedef.CodeInvalidValue, "migrate.allowed[0]"},
		{"agents", "migrate:\n  allowed: ['role:ask_admin', agents]\n", typedef.CodeInvalidValue, "migrate.allowed[1]"},
		{"an agent principal", "migrate:\n  allowed: ['principal:agent:bot']\n", typedef.CodeInvalidValue, "migrate.allowed[0]"},
		{"a customer", "migrate:\n  allowed: ['principal:customer:kim']\n", typedef.CodeInvalidValue, "migrate.allowed[0]"},
		{"the timer", "migrate:\n  allowed: ['principal:system:timer']\n", typedef.CodeInvalidValue, "migrate.allowed[0]"},
		{"a role with no name", "migrate:\n  allowed: ['role:']\n", typedef.CodeInvalidValue, "migrate.allowed[0]"},
		{"an entry twice", "migrate:\n  allowed: ['role:ask_admin', 'principal:user:ana', 'role:ask_admin']\n", typedef.CodeDuplicate, "migrate.allowed[2]"},
		{"a key the block does not have", "migrate:\n  allowed: ['role:ask_admin']\n  except: [creator]\n", typedef.CodeUnknownKey, "migrate.except"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ty, err := typedef.Parse([]byte(stAsk(2, c.block)))
			if err == nil {
				t.Fatalf("Parse loaded it (Migrate = %+v); want %s at %s", ty.Migrate, c.code, c.path)
			}
			wantOne(t, problemsOf(t, err), c.code, c.path, "ask", 2)
		})
	}
}

func TestMigrateKey_StoredForm(t *testing.T) {
	with := storedJSON(t, stAsk(2, "migrate:\n  allowed: ['role:ask_admin']\n"))
	if !strings.Contains(string(with), `"migrate":{"allowed":["role:ask_admin"]}`) {
		t.Errorf("stored as %s; want it to hold the migrate block", with)
	}
	back, err := typedef.FromJSON(with)
	if err != nil || back.Migrate == nil || !slices.Equal(back.Migrate.Allowed, []string{"role:ask_admin"}) {
		t.Errorf("read back: Migrate = %+v, %v", back.Migrate, err)
	}
	if without := storedJSON(t, stAsk(2, "")); strings.Contains(string(without), "migrate") {
		t.Errorf("a Type without the block stores %s; want no migrate key", without)
	}
}

// No Type of the repository has the key yet.
func TestMigrateKey_TheRepositoryTypes(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	for _, ty := range types {
		if ty.Migrate != nil {
			t.Errorf("%s@%d has a migrate block %+v; none should yet", ty.ID, ty.Version, ty.Migrate)
		}
	}
}
