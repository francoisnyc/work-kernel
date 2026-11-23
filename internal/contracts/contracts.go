// Package contracts is the action catalogue (Ventoux V1): every action a
// Type's hook may name, with its contract. A contract is a hook protocol
// operation (hookproto.Operation, the descriptor's format) plus the
// connector and major it belongs to: typed inputs and outputs in order,
// kinds, side effects, risk, and the reason codes a check may deny with.
//
// The files live in the repository's contracts/ folder and are compiled in
// (package contracts at the root). Once published a contract never
// changes: contracts.lock holds each one's digest, and CI refuses a changed
// or removed line. This package is pure: no network, no clock.
//
// Phase B stubs: nothing is implemented.
package contracts

import (
	"errors"
	"io/fs"

	"github.com/francoisnyc/work-kernel/internal/hookproto"
)

var errNotImplemented = errors.New("contracts: not implemented")

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
	Type        string   // one of the Type* constants
	Options     []string // enum only
	Nullable    bool
	Description string
	Trust       string // inputs only: any or trusted
}

// Action is the name a hook uses: <connector>.<operation>@<major>.
func (c Contract) Action() string { return "" }

// InputValues returns the inputs in order.
func (c Contract) InputValues() []Value { return nil }

// OutputValues returns the outputs in order.
func (c Contract) OutputValues() []Value { return nil }

// Digest is sha256:<hex> of the machine contract: the contract without its
// descriptions, as canonical JSON.
func (c Contract) Digest() string { return "" }

// Matches checks a connector's descriptor operation against the contract.
func (c Contract) Matches(op hookproto.Operation) error { return errNotImplemented }

// Check checks a value against its type: an output a connector answered.
func (v Value) Check(x any) error { return errNotImplemented }

// Parse reads one contract file, named <connector>.<operation>@<major>.json,
// and checks it.
func Parse(file string, data []byte) (Contract, error) { return Contract{}, errNotImplemented }

// Catalog is a set of contracts, by action.
type Catalog struct{}

// Load reads every contract file of fsys and contracts.lock, and checks
// that the two agree both ways.
func Load(fsys fs.FS) (*Catalog, error) { return nil, errNotImplemented }

// Default is the compiled-in catalogue.
func Default() *Catalog { return &Catalog{} }

// Lookup returns the contract a hook's action names:
// <connector>.<operation>[@<major>], no major meaning 1.
func (c *Catalog) Lookup(action string) (Contract, bool) { return Contract{}, false }

// All returns every contract, ordered by action.
func (c *Catalog) All() []Contract { return nil }

// ParseLock reads contracts.lock: action -> digest.
func ParseLock(data []byte) (map[string]string, error) { return nil, errNotImplemented }

// CheckFrozen compares the base branch's lock with this one: every line of
// base must be in head, unchanged. base may be empty (no lock yet).
func CheckFrozen(base, head []byte) error { return errNotImplemented }
