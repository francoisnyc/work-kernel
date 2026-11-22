package typedef

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Source is one draft Type file: its name (a path, say) and its YAML.
type Source struct {
	Name    string
	Content []byte
}

// Check runs every stage on the drafts, as ParseDir does on a folder, with
// the registered Type versions loaded beside them (Ventoux V2b). registered
// holds their stored definitions, the JSON RegisterType stores. Nothing is
// registered: Check is pure.
//
//   - A draft's problems name its Source and are positioned in its YAML.
//   - A draft that declares a registered id@version must have the same
//     definition (compared as JSON values, as Postgres compares jsonb):
//     else VERSION_CHANGED, a problem of the Types together (stage c).
//   - Stage (c) runs on the registered versions alone, then with the
//     drafts (each replacing its registered id@version); only new problems
//     are reported. A new problem on a registered version has no Source and
//     no position, only its Type and path: the caller names it.
//
// The error is Problems, or a plain error when a registered definition
// cannot be read (the store is broken, not the drafts). On success Check
// returns the drafts, ordered by Type ID, then version.
func Check(sources []Source, registered [][]byte) ([]*Type, error) {
	return check(sources, registered, nil)
}

// draft is a draft Type with its document and source, to place problems.
type draft struct {
	t      *Type
	doc    *yaml.Node
	source string
}

// check is Check with problems found before it (ParseDir's folder walk):
// they hold stage (c) back like any problem of stages (a) and (b).
func check(sources []Source, registered [][]byte, early Problems) ([]*Type, error) {
	// The registered versions first: an unreadable one is not the drafts'
	// problem, and no draft problem should hide it.
	type stored struct {
		t   *Type
		def []byte
	}
	regs := map[string]stored{} // "id@version"
	var regTypes []*Type
	for i, def := range registered {
		t, err := FromJSON(def)
		if err != nil {
			return nil, fmt.Errorf("typedef: registered definition %d of %d (%s) cannot be read: %v",
				i+1, len(registered), registeredName(def), err)
		}
		regs[key(t)] = stored{t, def}
		regTypes = append(regTypes, t)
	}

	// Stages (a) and (b), draft by draft.
	ps := slices.Clone(early)
	drafts := map[string]draft{}
	var types []*Type
	for _, src := range sources {
		t, doc, fps := parseDoc(src.Content)
		if t != nil {
			k := key(t)
			if first, dup := drafts[k]; dup {
				newReporter(&fps, t.ID, t.Version).fail(CodeDuplicateVersion, "version",
					"Give one of the two files the next version number, or remove it",
					"%s v%d is declared by both %s and %s", t.ID, t.Version, first.source, src.Name)
				position(fps, doc)
			} else {
				drafts[k] = draft{t, doc, src.Name}
				types = append(types, t)
			}
		}
		for i := range fps {
			fps[i].Source = src.Name
		}
		ps = append(ps, fps...)
	}
	if len(ps) > 0 {
		sortProblems(ps)
		return nil, ps
	}
	sortTypes(types)

	// Stage (c). A draft of a registered version must be the same version.
	for _, t := range types {
		reg, ok := regs[key(t)]
		if !ok {
			continue
		}
		same, err := sameDefinition(t, reg.def)
		if err != nil {
			return nil, err
		}
		if !same {
			var vps Problems
			newReporter(&vps, t.ID, t.Version).fail(CodeVersionChanged, "version",
				"Give the draft the next version number: Items are pinned to the version they were created with, so a version never changes",
				"%s v%d is already registered with a different definition", t.ID, t.Version)
			ps = append(ps, vps...)
		}
	}
	// The registered versions alone, then with the drafts; what was there
	// already is not the drafts'.
	before := map[string]bool{}
	for _, p := range together(sortedCopy(regTypes)) {
		before[problemKey(p)] = true
	}
	all := slices.Clone(types)
	for _, t := range regTypes {
		if _, replaced := drafts[key(t)]; !replaced {
			all = append(all, t)
		}
	}
	for _, p := range together(sortedCopy(all)) {
		if !before[problemKey(p)] {
			ps = append(ps, p)
		}
	}
	if len(ps) == 0 {
		return types, nil
	}
	for i := range ps {
		if d, ok := drafts[fmt.Sprintf("%s@%d", ps[i].TypeID, ps[i].Version)]; ok {
			ps[i].Source = d.source
			ps[i].Line, ps[i].Column = locate(d.doc, ps[i].Path)
		}
	}
	sortProblems(ps)
	return nil, ps
}

// together runs the checks of the Types together (stage c) on types,
// ordered by ID, then version, so a problem across versions lands on the
// later one.
func together(types []*Type) Problems {
	var ps Problems
	for _, check := range []func([]*Type) error{
		// Links name other Types, and keep their inverses across versions.
		CheckLinkTargets, CheckLinkInverses,
		// So do incoming declarations: the links other Types declare.
		CheckIncoming,
		// One value or several is part of what a field is, in every version.
		CheckFieldShapes,
		// Reactions and link resolution name other Types too.
		CheckReactions,
		// So do offers: a host offers what the hooks of the Items it creates
		// inside itself read from their container.
		CheckOffers,
		// And what a container admits customers to.
		CheckCustomers,
	} {
		var cps Problems
		if errors.As(check(types), &cps) {
			ps = append(ps, cps...)
		}
	}
	return ps
}

func key(t *Type) string { return fmt.Sprintf("%s@%d", t.ID, t.Version) }

// problemKey identifies a problem across two runs of stage (c).
func problemKey(p Problem) string {
	return fmt.Sprintf("%s %s@%d %s", p.Code, p.TypeID, p.Version, p.Path)
}

// sortTypes orders Types by ID, then version. cmp.Or picks the first
// non-zero comparison.
func sortTypes(types []*Type) {
	slices.SortFunc(types, func(a, b *Type) int {
		return cmp.Or(strings.Compare(a.ID, b.ID), cmp.Compare(a.Version, b.Version))
	})
}

func sortedCopy(types []*Type) []*Type {
	out := slices.Clone(types)
	sortTypes(out)
	return out
}

// sameDefinition reports whether t, stored as RegisterType stores it, has
// the definition def: the same JSON value, whatever the key order or the
// spacing (as Postgres compares jsonb).
func sameDefinition(t *Type, def []byte) (bool, error) {
	mine, err := json.Marshal(t)
	if err != nil {
		return false, fmt.Errorf("typedef: encode %s: %w", key(t), err)
	}
	var a, b any
	if err := json.Unmarshal(mine, &a); err != nil {
		return false, err
	}
	if err := json.Unmarshal(def, &b); err != nil {
		return false, err
	}
	return reflect.DeepEqual(a, b), nil
}

// registeredName names a stored definition as well as it can be read.
func registeredName(def []byte) string {
	var head struct {
		ID      any `json:"id"`
		Version any `json:"version"`
	}
	if json.Unmarshal(def, &head) != nil || head.ID == nil {
		return "unreadable JSON"
	}
	return fmt.Sprintf("registered %v v%v", head.ID, head.Version)
}
