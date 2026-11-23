// Package contracts is the action catalogue (Ventoux V1): every action a
// Type's hook may name, with its contract. A contract is a hook protocol
// operation (hookproto.Operation, the descriptor's format) plus the
// connector and major it belongs to: typed inputs and outputs in order,
// kinds, side effects, risk, and the reason codes a check may deny with.
//
// The files live in the repository's contracts/ folder and are compiled in
// (package contracts at the root), so the item service checks a Type
// against exactly the contracts the worker runs. Once published a contract
// never changes: contracts.lock holds each one's digest, Load refuses a
// file that differs from its line, and CI refuses a line that changed or
// went (CheckFrozen). This package is pure: no network, no clock.
package contracts

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	files "github.com/francoisnyc/work-kernel/contracts"
	"github.com/francoisnyc/work-kernel/internal/hookproto"
	"github.com/francoisnyc/work-kernel/internal/item"
)

// LockFile is the lock's name in the contracts folder.
const LockFile = "contracts.lock"

// Contract is one action's contract, as its file holds it.
//
// (Java readers: embedding hookproto.Operation promotes its fields, Name,
// Kinds and the rest, into Contract, and encoding/json reads and writes them
// as if declared here: composition, not inheritance.)
type Contract struct {
	Connector string `json:"connector"`
	Major     int    `json:"major"`
	hookproto.Operation
}

// The value types of an input or output, named as the field types a hook
// may read into it or write it to.
const (
	TypeString    = "string"
	TypeInt       = "int"
	TypeBool      = "bool"
	TypeEnum      = "enum"
	TypePrincipal = "principal"
	TypeDate      = "date"
	TypeDateTime  = "datetime"
)

// Value is one input or output, read from its JSON Schema property.
type Value struct {
	Name        string
	Type        string   // one of the Type* constants; empty only in a test's declaration
	Options     []string // enum only
	Nullable    bool
	Description string
	Trust       string // inputs only: any or trusted
}

// Action is the name a hook uses: <connector>.<operation>@<major>.
func (c Contract) Action() string { return fmt.Sprintf("%s.%s@%d", c.Connector, c.Name, c.Major) }

// InputValues returns the inputs in order.
func (c Contract) InputValues() []Value { return values(c.Inputs, c.InputSchema.Properties, true) }

// OutputValues returns the outputs in order.
func (c Contract) OutputValues() []Value {
	var props map[string]hookproto.Property
	if c.OutputSchema != nil {
		props = c.OutputSchema.Properties
	}
	return values(c.Outputs, props, false)
}

func values(order []string, props map[string]hookproto.Property, input bool) []Value {
	out := make([]Value, 0, len(order))
	for _, name := range order {
		p := props[name]
		v := Value{Name: name, Description: p.Description, Options: p.Enum}
		base, nullable := baseType(p.Type)
		v.Nullable = nullable
		switch base {
		case "string":
			switch {
			case len(p.Enum) > 0:
				v.Type = TypeEnum
			case p.Format == hookproto.FormatPrincipal:
				v.Type = TypePrincipal
			case p.Format == hookproto.FormatDate:
				v.Type = TypeDate
			case p.Format == hookproto.FormatDateTime:
				v.Type = TypeDateTime
			default:
				v.Type = TypeString
			}
		case "integer":
			v.Type = TypeInt
		case "boolean":
			v.Type = TypeBool
		}
		if input {
			v.Trust = p.Trust
			if v.Trust == "" {
				v.Trust = hookproto.TrustAny
			}
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// baseType returns the type list's one type other than null, and whether
// null is in it.
func baseType(types hookproto.TypeList) (base string, nullable bool) {
	for _, t := range types {
		if t == "null" {
			nullable = true
		} else {
			base = t
		}
	}
	return base, nullable
}

// Check checks a value against its type: an output a connector answered,
// as JSON decodes it (a whole float64 is an integer). An untyped value (a
// test's declaration) takes anything.
func (v Value) Check(x any) error {
	if x == nil {
		if v.Nullable || v.Type == "" {
			return nil
		}
		return fmt.Errorf("%s is null, which it may not be", v.Name)
	}
	bad := func(want string) error { return fmt.Errorf("%s is %#v, not %s", v.Name, x, want) }
	switch v.Type {
	case "":
		return nil
	case TypeInt:
		switch n := x.(type) {
		case int, int32, int64:
			return nil
		case float64:
			if n == math.Trunc(n) && math.Abs(n) < 1<<53 {
				return nil
			}
		}
		return bad("a whole number")
	case TypeBool:
		if _, ok := x.(bool); ok {
			return nil
		}
		return bad("true or false")
	}
	s, ok := x.(string)
	if !ok {
		return bad("a string")
	}
	switch v.Type {
	case TypeEnum:
		if !slices.Contains(v.Options, s) {
			return bad("one of " + strings.Join(v.Options, ", "))
		}
	case TypePrincipal:
		if _, ok := item.KindOf(s); !ok {
			return bad("a principal ID (user:<id>, agent:<id>, ...)")
		}
	case TypeDate:
		if d, err := time.Parse(time.DateOnly, s); err != nil || d.Format(time.DateOnly) != s {
			return bad("a date (2026-12-01)")
		}
	case TypeDateTime:
		if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
			return bad("an instant with an offset (RFC 3339)")
		}
	}
	return nil
}

// Digest is sha256:<hex> of the machine contract: the contract without its
// descriptions, as canonical JSON (keys sorted, no spaces), so neither key
// order, spacing nor a description's wording changes it.
func (c Contract) Digest() string {
	m := c
	m.Description = ""
	m.InputSchema.Properties = undescribed(c.InputSchema.Properties)
	if c.OutputSchema != nil {
		out := *c.OutputSchema
		out.Properties = undescribed(out.Properties)
		m.OutputSchema = &out
	}
	data, err := json.Marshal(m)
	if err != nil {
		panic("contracts: marshal a contract: " + err.Error())
	}
	// Through a generic value and back: maps marshal with sorted keys, so
	// the bytes do not depend on the Go structs' field order.
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		panic("contracts: " + err.Error())
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		panic("contracts: " + err.Error())
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func undescribed(props map[string]hookproto.Property) map[string]hookproto.Property {
	out := make(map[string]hookproto.Property, len(props))
	for k, p := range props {
		p.Description = ""
		out[k] = p
	}
	return out
}

// Matches checks a connector's descriptor operation against the contract:
// its kinds include the contract's; its inputs and outputs are the
// contract's names, each with the contract's type list, and the contract's
// format and enum where the descriptor gives them (a descriptor written
// before V1 has neither); its side effects are the contract's; its errors
// are among the contract's. An untyped value (a test's declaration) is
// compared by name only.
func (c Contract) Matches(op hookproto.Operation) error {
	if op.Name != c.Name {
		return fmt.Errorf("it is operation %s, not %s", op.Name, c.Name)
	}
	for _, k := range c.Kinds {
		if !slices.Contains(op.Kinds, k) {
			return fmt.Errorf("%s is not a %s", c.Name, k)
		}
	}
	var outProps map[string]hookproto.Property
	if op.OutputSchema != nil {
		outProps = op.OutputSchema.Properties
	}
	var contractOut map[string]hookproto.Property
	if c.OutputSchema != nil {
		contractOut = c.OutputSchema.Properties
	}
	if err := sameProperties("input", c.InputSchema.Properties, op.InputSchema.Properties); err != nil {
		return err
	}
	if err := sameProperties("output", contractOut, outProps); err != nil {
		return err
	}
	if op.SideEffects != c.SideEffects {
		return fmt.Errorf("its sideEffects is %q, the contract's %q", op.SideEffects, c.SideEffects)
	}
	for _, e := range op.Errors {
		if !slices.Contains(c.Errors, e) {
			return fmt.Errorf("it declares the reason %s, which the contract does not", e)
		}
	}
	return nil
}

func sameProperties(what string, contract, desc map[string]hookproto.Property) error {
	for name := range desc {
		if _, ok := contract[name]; !ok {
			return fmt.Errorf("it has the %s %s, which the contract does not", what, name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(contract)) {
		want := contract[name]
		got, ok := desc[name]
		if !ok {
			return fmt.Errorf("it has no %s %s", what, name)
		}
		if len(want.Type) == 0 {
			continue // untyped: a test's declaration
		}
		if !slices.Equal(slices.Sorted(slices.Values(got.Type)), slices.Sorted(slices.Values(want.Type))) {
			return fmt.Errorf("its %s %s is of type %v, the contract's %v", what, name, []string(got.Type), []string(want.Type))
		}
		if got.Format != "" && got.Format != want.Format {
			return fmt.Errorf("its %s %s has format %q, the contract's %q", what, name, got.Format, want.Format)
		}
		if got.Enum != nil && !slices.Equal(got.Enum, want.Enum) {
			return fmt.Errorf("its %s %s has enum %v, the contract's %v", what, name, got.Enum, want.Enum)
		}
	}
	return nil
}

// --- names ---

var (
	namePattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	reasonPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`) // the item service's reason codes
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// parseName reads <connector>.<operation>[@<major>]; without a major, it is
// 1 unless the major is required.
func parseName(s string, majorRequired bool) (connector, operation string, major int, ok bool) {
	base, m, versioned := strings.Cut(s, "@")
	major = 1
	if versioned {
		n, err := strconv.Atoi(m)
		if err != nil || n < 1 || strconv.Itoa(n) != m {
			return "", "", 0, false
		}
		major = n
	} else if majorRequired {
		return "", "", 0, false
	}
	connector, operation, found := strings.Cut(base, ".")
	if !found || !namePattern.MatchString(connector) || !namePattern.MatchString(operation) {
		return "", "", 0, false
	}
	return connector, operation, major, true
}

// --- parsing ---

// Parse reads one contract file, named <connector>.<operation>@<major>.json,
// and checks it. Every error starts with the file's name. An input without
// x-pli-trust is read as any.
func Parse(file string, data []byte) (Contract, error) {
	fail := func(format string, args ...any) (Contract, error) {
		return Contract{}, fmt.Errorf("%s: %s", file, fmt.Sprintf(format, args...))
	}
	var c Contract
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return fail("not a contract: %v", err)
	}
	if err := c.check(); err != nil {
		return fail("%v", err)
	}
	if want := c.Action() + ".json"; file != want {
		return fail("holds %s; name the file %s", c.Action(), want)
	}
	for name, p := range c.InputSchema.Properties {
		if p.Trust == "" {
			p.Trust = hookproto.TrustAny
			c.InputSchema.Properties[name] = p
		}
	}
	if c.OutputSchema != nil && len(c.OutputSchema.Properties) == 0 {
		c.OutputSchema = nil
	}
	return c, nil
}

// check applies the format's rules.
func (c Contract) check() error {
	switch {
	case !namePattern.MatchString(c.Connector):
		return fmt.Errorf("connector %q is not a name (lower case letters, digits, _ and -)", c.Connector)
	case !namePattern.MatchString(c.Name):
		return fmt.Errorf("operation %q is not a name (lower case letters, digits, _ and -)", c.Name)
	case c.Major < 1:
		return fmt.Errorf("major %d: a major is 1 or more", c.Major)
	case strings.TrimSpace(c.Description) == "":
		return errors.New("no description: say what the action does")
	}
	if err := checkKinds(c.Kinds, c.SideEffects); err != nil {
		return err
	}
	if c.Async != hookproto.AsyncForbidden {
		return fmt.Errorf("async %q: asynchronous completion is not built, so async is forbidden", c.Async)
	}
	if err := checkReasons(c.Errors, slices.Contains(c.Kinds, hookproto.KindCheck)); err != nil {
		return err
	}
	switch c.Risk {
	case hookproto.RiskLow, hookproto.RiskMedium, hookproto.RiskHigh:
	case "":
		return errors.New("no risk: give low, medium or high")
	default:
		return fmt.Errorf("risk %q: give low, medium or high", c.Risk)
	}
	if err := checkSchema("inputSchema", "input", &c.InputSchema, c.Inputs, true); err != nil {
		return err
	}
	out := c.OutputSchema
	if out == nil {
		out = &hookproto.Schema{Type: "object"}
	}
	if err := checkSchema("outputSchema", "output", out, c.Outputs, false); err != nil {
		return err
	}
	if slices.Equal(c.Kinds, []hookproto.Kind{hookproto.KindEnrich}) && len(c.Outputs) == 0 {
		return errors.New("an enrich operation has at least one output: what it returns is what it is for")
	}
	return nil
}

func checkKinds(kinds []hookproto.Kind, sideEffects string) error {
	if len(kinds) == 0 {
		return errors.New("no kinds: give check, enrich or action")
	}
	seen := map[hookproto.Kind]bool{}
	for _, k := range kinds {
		switch k {
		case hookproto.KindCheck, hookproto.KindEnrich, hookproto.KindAction:
		default:
			return fmt.Errorf("kind %q: give check, enrich or action", k)
		}
		if seen[k] {
			return fmt.Errorf("kind %s twice", k)
		}
		seen[k] = true
	}
	action := seen[hookproto.KindAction]
	if action && len(kinds) > 1 {
		return errors.New("an action is its operation's only kind: a side effect is never a check or an enrich")
	}
	switch sideEffects {
	case hookproto.SideEffectsNone, hookproto.SideEffectsSome:
	default:
		return fmt.Errorf("sideEffects %q: give none or some", sideEffects)
	}
	if action && sideEffects != hookproto.SideEffectsSome {
		return errors.New("an action has sideEffects: some")
	}
	if !action && sideEffects == hookproto.SideEffectsSome {
		return errors.New("only an action has sideEffects: some")
	}
	return nil
}

func checkReasons(reasons []string, check bool) error {
	if len(reasons) > 0 && !check {
		return errors.New("errors: only a check denies, with a reason")
	}
	seen := map[string]bool{}
	for _, r := range reasons {
		if !reasonPattern.MatchString(r) {
			return fmt.Errorf("errors: %q is not a reason code (lower case letters, digits and _)", r)
		}
		if seen[r] {
			return fmt.Errorf("errors: %s twice", r)
		}
		seen[r] = true
	}
	return nil
}

// checkSchema checks one schema and its order list.
func checkSchema(key, what string, s *hookproto.Schema, order []string, input bool) error {
	if s.Type != "object" {
		return fmt.Errorf("%s: type %q: a schema is an object", key, s.Type)
	}
	if s.Required != nil || s.AdditionalProperties != nil {
		return fmt.Errorf("%s: required and additionalProperties are not part of a contract: every %s is always sent", key, what)
	}
	listed := map[string]bool{}
	for _, name := range order {
		if listed[name] {
			return fmt.Errorf("%ss: %s twice", what, name)
		}
		listed[name] = true
		if _, ok := s.Properties[name]; !ok {
			return fmt.Errorf("%ss: %s is not in %s", what, name, key)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(s.Properties)) {
		if !listed[name] {
			return fmt.Errorf("%s %s is not in %ss, the order a hook maps to", what, name, what)
		}
		if err := checkProperty(what+" "+name, s.Properties[name], input); err != nil {
			return err
		}
	}
	return nil
}

func checkProperty(what string, p hookproto.Property, input bool) error {
	if strings.TrimSpace(p.Description) == "" {
		return fmt.Errorf("%s: no description", what)
	}
	var base string
	for _, t := range p.Type {
		switch {
		case t == "null":
		case base != "":
			return fmt.Errorf("%s: type %v: one type, with null or not", what, []string(p.Type))
		default:
			base = t
		}
	}
	switch base {
	case "string", "integer", "boolean":
	case "":
		return fmt.Errorf("%s: no type: give string, integer or boolean", what)
	default:
		return fmt.Errorf("%s: type %s: give string, integer or boolean", what, base)
	}
	if p.Format != "" {
		if base != "string" {
			return fmt.Errorf("%s: format is for a string", what)
		}
		switch p.Format {
		case hookproto.FormatPrincipal, hookproto.FormatDate, hookproto.FormatDateTime:
		default:
			return fmt.Errorf("%s: format %q: give pli-principal, date or date-time", what, p.Format)
		}
	}
	if p.Enum != nil {
		switch {
		case base != "string":
			return fmt.Errorf("%s: enum is for a string", what)
		case len(p.Enum) == 0:
			return fmt.Errorf("%s: an empty enum", what)
		case p.Format != "":
			return fmt.Errorf("%s: an enum has no format", what)
		}
		seen := map[string]bool{}
		for _, v := range p.Enum {
			if seen[v] {
				return fmt.Errorf("%s: enum value %q twice", what, v)
			}
			seen[v] = true
		}
	}
	switch {
	case !input && p.Trust != "":
		return fmt.Errorf("%s: x-pli-trust is for an input", what)
	case input && p.Trust != "" && p.Trust != hookproto.TrustAny && p.Trust != hookproto.TrustTrusted:
		return fmt.Errorf("%s: x-pli-trust %q: give any or trusted", what, p.Trust)
	}
	return nil
}

// --- the catalogue ---

// Catalog is a set of contracts, by action.
type Catalog struct {
	byAction map[string]Contract
	all      []Contract
}

// Load reads every contract file of fsys (*.json at its root) and
// contracts.lock, and checks that the two agree both ways: each contract
// has its line, with its digest, and each line its contract.
func Load(fsys fs.FS) (*Catalog, error) {
	lockData, err := fs.ReadFile(fsys, LockFile)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", LockFile, err)
	}
	lock, err := ParseLock(lockData)
	if err != nil {
		return nil, err
	}
	names, err := fs.Glob(fsys, "*.json")
	if err != nil {
		return nil, err
	}
	cat := &Catalog{byAction: map[string]Contract{}}
	var problems []error
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		c, err := Parse(name, data)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		switch digest, ok := lock[c.Action()]; {
		case !ok:
			problems = append(problems, fmt.Errorf("%s: no line in %s: publish it with %s %s", c.Action(), LockFile, c.Action(), c.Digest()))
		case digest != c.Digest():
			problems = append(problems, fmt.Errorf("%s: its digest is %s, but %s says %s: a published contract never changes; a change is a new major",
				c.Action(), c.Digest(), LockFile, digest))
		}
		cat.byAction[c.Action()] = c
	}
	for _, action := range slices.Sorted(maps.Keys(lock)) {
		if _, ok := cat.byAction[action]; !ok {
			problems = append(problems, fmt.Errorf("%s: in %s, but there is no %s.json", action, LockFile, action))
		}
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	for _, action := range slices.Sorted(maps.Keys(cat.byAction)) {
		cat.all = append(cat.all, cat.byAction[action])
	}
	return cat, nil
}

// Default is the compiled-in catalogue (contracts/). It panics if the
// files do not load: go test refuses that first (the catalogue tests).
var Default = sync.OnceValue(func() *Catalog {
	cat, err := Load(files.FS)
	if err != nil {
		panic("contracts: the compiled-in catalogue does not load: " + err.Error())
	}
	return cat
})

// Lookup returns the contract a hook's action names:
// <connector>.<operation>[@<major>], no major meaning 1.
func (c *Catalog) Lookup(action string) (Contract, bool) {
	connector, operation, major, ok := parseName(action, false)
	if !ok {
		return Contract{}, false
	}
	contract, ok := c.byAction[fmt.Sprintf("%s.%s@%d", connector, operation, major)]
	return contract, ok
}

// All returns every contract, ordered by action.
func (c *Catalog) All() []Contract { return slices.Clone(c.all) }

// --- the lock ---

// ParseLock reads contracts.lock: one "<action> sha256:<hex>" per line,
// # comments and blank lines skipped. It returns action -> digest.
func ParseLock(data []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || !digestPattern.MatchString(f[1]) {
			return nil, fmt.Errorf("%s:%d: %q: want <action> sha256:<hex>", LockFile, n, line)
		}
		if _, _, _, ok := parseName(f[0], true); !ok {
			return nil, fmt.Errorf("%s:%d: %q is not <connector>.<operation>@<major>", LockFile, n, f[0])
		}
		if _, dup := out[f[0]]; dup {
			return nil, fmt.Errorf("%s:%d: %s twice", LockFile, n, f[0])
		}
		out[f[0]] = f[1]
	}
	return out, sc.Err()
}

// CheckFrozen compares the base branch's lock with this one: every line of
// base must be in head, unchanged. A new line is how a contract is
// published. base may be empty (no lock yet).
func CheckFrozen(base, head []byte) error {
	was, err := ParseLock(base)
	if err != nil {
		return fmt.Errorf("the base branch's lock: %w", err)
	}
	now, err := ParseLock(head)
	if err != nil {
		return err
	}
	var problems []error
	for _, action := range slices.Sorted(maps.Keys(was)) {
		switch digest, ok := now[action]; {
		case !ok:
			problems = append(problems, fmt.Errorf("%s was published and is gone: a published contract is never removed", action))
		case digest != was[action]:
			problems = append(problems, fmt.Errorf("%s was published as %s and is now %s: a published contract never changes; a change is a new major",
				action, was[action], digest))
		}
	}
	return errors.Join(problems...)
}
