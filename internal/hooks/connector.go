package hooks

import (
	"context"
	"errors"
	"fmt"

	"github.com/francoisnyc/work-kernel/internal/hookproto"
)

// The hook protocol, worker side (pli.hooks/v1). A Type's hook names an
// action, <connector>.<operation>[@<major>] (no major means 1). The
// connector at that major is either compiled into the worker or a remote
// service; configuration, never code, says which (a connectors file,
// re-read when it changes). Either way it is a Connector: its request and
// response are the protocol's envelopes, so a Type cannot tell the two
// apart.

// Connector is one connector at one major version.
//
// (Java readers: an interface is satisfied implicitly. Any type with this
// method is a Connector; nothing declares "implements".)
type Connector interface {
	Handle(ctx context.Context, req hookproto.Request) (hookproto.Response, error)
}

// Errors a remote connector's configuration or contract can cause. Each is
// permanent (errors.Is finds ErrPermanent too), and each has its own reason
// on the hold it fails: a misconfiguration shows at once instead of using
// up the hook's budget.
var (
	// ErrConnectorUnconfigured: no connection is configured for the
	// connector@major the action names, and none is compiled in.
	ErrConnectorUnconfigured = fmt.Errorf("hooks: connector not configured (%w)", ErrPermanent)
	// ErrConnectorUnauthorized: the connector refused the worker's
	// signature or credential (401 or 403).
	ErrConnectorUnauthorized = fmt.Errorf("hooks: connector refused the worker's signature (%w)", ErrPermanent)
	// ErrConnectorMismatch: the connector at the configured URL is not the
	// one the catalog declares (another name or major, a missing operation
	// or kind, other input or output names, no pli.hooks/v1), or it
	// answered a deny reason its descriptor does not declare.
	ErrConnectorMismatch = fmt.Errorf("hooks: connector does not match its declaration (%w)", ErrPermanent)
)

// errStub marks a Phase B stub.
var errStub = errors.New("hooks: not implemented")

// Connection is where a remote connector is and how to sign for it.
type Connection struct {
	URL    string // base URL; the protocol's paths are relative to it
	Secret string // whsec_<base64>; never sent, only used to sign
	Ref    string // the connection's name in envelopes; empty: the connector@major key
}

// Config is a connectors file, parsed:
//
//	connectors:
//	  hr@1: {url: http://connstub:8083, secret_env: HR_HOOK_SECRET, ref: hr-directory}
//
// A connector not listed stays compiled in (if it is).
type Config struct {
	Connectors map[string]Connection // connector@major -> connection
}

// LoadConfig reads a connectors file. getenv resolves each secret_env (an
// unset or empty one is an error naming it). Unknown keys, a key that is
// not <connector>@<major>, or a connection without a URL are errors that
// name the file.
func LoadConfig(path string, getenv func(string) string) (Config, error) {
	return Config{}, errStub
}

// WithConnection returns connectors that reach connector@major (hr@1)
// through conn.
func (c *Connectors) WithConnection(connectorAtMajor string, conn Connection) *Connectors {
	return c
}

// WithConfigFile returns connectors configured by the connectors file at
// path. The file is read now (an error is returned) and again on any call
// after its modification time changes; a later read that fails keeps the
// last good configuration and logs the error. Secrets are read from the
// environment.
func (c *Connectors) WithConfigFile(path string) (*Connectors, error) {
	return c, errStub
}

// Connector returns the connector for connector@major (crm@1, hr@1): the
// configured remote one, else the compiled-in one, else
// ErrConnectorUnconfigured.
func (c *Connectors) Connector(connectorAtMajor string) (Connector, error) {
	return nil, errStub
}

// declareRemote adds a remote operation's declaration to the catalog:
// <connector>.<operation>@<major> (no major: 1), the kinds it serves, and
// its input and output names in the order hooks map reads and writes to
// them. Only package code and tests (export_test.go) call it.
func declareRemote(action string, kinds []hookproto.Kind, inputs, outputs []string) {}
