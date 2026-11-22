package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

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

// --- action names ---

// actionRef is a parsed action name.
type actionRef struct {
	connector, operation string
	major                int
}

// namePattern is a connector's or an operation's name: no '.', '@' or space.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// parseAction reads <connector>.<operation>[@<major>]; no major means 1.
func parseAction(s string) (actionRef, bool) {
	base, major, versioned := strings.Cut(s, "@")
	ref := actionRef{major: 1}
	if versioned {
		n, ok := parseMajor(major)
		if !ok {
			return actionRef{}, false
		}
		ref.major = n
	}
	var ok bool
	ref.connector, ref.operation, ok = strings.Cut(base, ".")
	if !ok || !namePattern.MatchString(ref.connector) || !namePattern.MatchString(ref.operation) {
		return actionRef{}, false
	}
	return ref, true
}

// parseMajor reads a major version: a positive whole number, written
// plainly ("1", not "01" or "+1").
func parseMajor(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || strconv.Itoa(n) != s {
		return 0, false
	}
	return n, true
}

func (r actionRef) key() string              { return fmt.Sprintf("%s.%s@%d", r.connector, r.operation, r.major) }
func (r actionRef) connectorAtMajor() string { return fmt.Sprintf("%s@%d", r.connector, r.major) }

// parseConnectorAtMajor reads <connector>@<major>.
func parseConnectorAtMajor(s string) (string, int, bool) {
	connector, major, ok := strings.Cut(s, "@")
	if !ok || !namePattern.MatchString(connector) {
		return "", 0, false
	}
	n, ok := parseMajor(major)
	return connector, n, ok
}

// declareRemote adds a remote operation's declaration to the catalog:
// <connector>.<operation>@<major> (no major: 1), the kinds it serves, and
// its input and output names in the order hooks map reads and writes to
// them. Only package code and tests (export_test.go) call it.
func declareRemote(actionName string, kinds []hookproto.Kind, inputs, outputs []string) {
	ref, ok := parseAction(actionName)
	if !ok {
		panic("hooks: declareRemote: bad action name " + actionName)
	}
	catalogMu.Lock()
	defer catalogMu.Unlock()
	catalog[ref.key()] = declared(kinds, inputs, outputs, action{remote: true})
}

// --- resolving a connector ---

// shared is what copies of one Connectors share.
type shared struct {
	mu          sync.Mutex
	descriptors map[string]hookproto.Descriptor // base URL -> its descriptor, once read
}

func newShared() *shared { return &shared{descriptors: map[string]hookproto.Descriptor{}} }

// Connection is where a remote connector is and how to sign for it.
type Connection struct {
	URL    string // base URL; the protocol's paths are relative to it
	Secret string // whsec_<base64>; never sent, only used to sign
	Ref    string // the connection's name in envelopes; empty: the connector@major key
}

// WithConnection returns connectors that reach connector@major (hr@1)
// through conn.
func (c *Connectors) WithConnection(connectorAtMajor string, conn Connection) *Connectors {
	with := *c
	with.conns = maps.Clone(c.conns)
	if with.conns == nil {
		with.conns = map[string]Connection{}
	}
	if conn.Ref == "" {
		conn.Ref = connectorAtMajor
	}
	with.conns[connectorAtMajor] = conn
	return &with
}

// Connector returns the connector for connector@major (crm@1, hr@1): the
// configured remote one, else the compiled-in one, else
// ErrConnectorUnconfigured.
func (c *Connectors) Connector(connectorAtMajor string) (Connector, error) {
	conn, _, err := c.resolve(connectorAtMajor)
	return conn, err
}

// resolve returns the connector for connector@major and its connection's
// name. The connectors file is read again first if it changed; a
// connection named in code (WithConnection) wins over the file.
func (c *Connectors) resolve(connectorAtMajor string) (Connector, string, error) {
	connector, major, ok := parseConnectorAtMajor(connectorAtMajor)
	if !ok {
		return nil, "", fmt.Errorf("%w: %q is not <connector>@<major>", ErrConnectorUnconfigured, connectorAtMajor)
	}
	conn, ok := c.conns[connectorAtMajor]
	if !ok && c.file != nil {
		conn, ok = c.file.connection(connectorAtMajor)
	}
	if ok {
		return &remote{c: c, connector: connector, major: major, conn: conn}, conn.Ref, nil
	}
	if compiledIn(connector, major) {
		return &builtin{c: c, connector: connector, major: major}, connectorAtMajor, nil
	}
	return nil, "", fmt.Errorf("%w: no connection for %s", ErrConnectorUnconfigured, connectorAtMajor)
}

// compiledIn reports whether the worker has code for connector@major.
func compiledIn(connector string, major int) bool {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	for key, a := range catalog {
		ref, _ := parseAction(key)
		if ref.connector == connector && ref.major == major && !a.remote {
			return true
		}
	}
	return false
}

// declaration returns the catalog's entry for an operation of connector@major.
func declaration(connector string, major int, operation string) (action, bool) {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	a, ok := catalog[actionRef{connector: connector, operation: operation, major: major}.key()]
	return a, ok
}

// --- compiled in ---

// builtin is a compiled-in connector: the envelope in, the action's code
// run with its inputs in declared order, the envelope out.
type builtin struct {
	c         *Connectors
	connector string
	major     int
}

func (b *builtin) Handle(ctx context.Context, req hookproto.Request) (hookproto.Response, error) {
	a, ok := declaration(b.connector, b.major, req.Hook.Operation)
	if !ok || a.remote {
		return hookproto.Response{}, permanent("%s@%d has no operation %q", b.connector, b.major, req.Hook.Operation)
	}
	name := b.connector + "." + req.Hook.Operation
	if !slices.Contains(a.kinds, req.Kind) {
		return hookproto.Response{}, permanent("%s is not a %s", name, req.Kind)
	}
	args, err := positional(name, a.inputs, req.Inputs)
	if err != nil {
		return hookproto.Response{}, err
	}
	key := req.IdempotencyKey
	if a.sideEffect && key == "" {
		return hookproto.Response{}, permanent("%s has a side effect and needs an idempotency key", name)
	}
	if b.c.noKeys {
		key = ""
	}
	var res Result
	if a.runCall != nil {
		_, hookID, _ := strings.Cut(req.Hook.Binding, "#")
		res, err = a.runCall(ctx, b.c, key, args, Call{HookID: hookID, ItemID: req.Item.ID})
	} else {
		res, err = a.run(ctx, b.c, key, args)
	}
	if err != nil {
		return hookproto.Response{}, err
	}
	out := hookproto.Response{Protocol: hookproto.Protocol, RequestID: req.RequestID}
	patch := func() map[string]any {
		if len(a.outputs) == 0 {
			return nil
		}
		p := make(map[string]any, len(a.outputs))
		for i, o := range a.outputs {
			if i < len(res.Values) {
				p[o] = res.Values[i]
			}
		}
		return p
	}
	switch {
	case req.Kind != hookproto.KindCheck:
		out.Outcome, out.Patch = hookproto.OutcomeOK, patch()
	case res.Verdict == Allow:
		out.Outcome = hookproto.OutcomeAllow
	case res.Verdict == Deny:
		out.Outcome, out.Reason = hookproto.OutcomeDeny, &hookproto.Reason{Code: res.Reason}
	case res.Verdict == Correct:
		out.Outcome, out.Patch = hookproto.OutcomeCorrect, patch()
	default:
		return hookproto.Response{}, permanent("%s: unknown verdict %q", name, res.Verdict)
	}
	return out, nil
}

// positional reads an envelope's inputs in the declared order: exactly the
// declared names, each present (null for unset).
func positional(name string, declared []string, inputs map[string]any) ([]any, error) {
	for k := range inputs {
		if !slices.Contains(declared, k) {
			return nil, permanent("%s takes no input %q", name, k)
		}
	}
	args := make([]any, len(declared))
	for i, k := range declared {
		v, ok := inputs[k]
		if !ok {
			return nil, permanent("%s: input %q is missing", name, k)
		}
		args[i] = v
	}
	return args, nil
}

// --- remote ---

// remote is a connector service reached over HTTPS (pli.hooks/v1).
type remote struct {
	c         *Connectors
	connector string
	major     int
	conn      Connection
}

func (r *remote) Handle(ctx context.Context, req hookproto.Request) (hookproto.Response, error) {
	a, ok := declaration(r.connector, r.major, req.Hook.Operation)
	if !ok {
		return hookproto.Response{}, permanent("%s@%d declares no operation %q", r.connector, r.major, req.Hook.Operation)
	}
	d, err := r.descriptor(ctx)
	if err != nil {
		return hookproto.Response{}, err
	}
	op, err := r.matches(d, a, req.Hook.Operation)
	if err != nil {
		return hookproto.Response{}, err
	}
	secret, err := hookproto.ParseSecret(r.conn.Secret)
	if err != nil {
		return hookproto.Response{}, fmt.Errorf("%w: the secret for %s@%d: %v", ErrConnectorUnconfigured, r.connector, r.major, err)
	}

	body, err := json.Marshal(req)
	if err != nil {
		return hookproto.Response{}, permanent("encode request: %v", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, r.conn.URL+hookproto.HooksPath, bytes.NewReader(body))
	if err != nil {
		return hookproto.Response{}, permanent("build request: %v", err)
	}
	httpReq.Header.Set("Content-Type", hookproto.ContentType)
	httpReq.Header.Set("Idempotency-Key", req.IdempotencyKey)
	hookproto.SetSignatureHeaders(httpReq.Header, secret, hookproto.WebhookID(req.IdempotencyKey), time.Now(), body)
	res, err := r.c.http.Do(httpReq)
	if err != nil {
		return hookproto.Response{}, fmt.Errorf("%s@%d %s: %w", r.connector, r.major, req.Hook.Operation, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return hookproto.Response{}, fmt.Errorf("%s@%d: read answer: %w", r.connector, r.major, err)
	}
	if res.StatusCode != http.StatusOK {
		return hookproto.Response{}, r.failure(res, data)
	}
	var out hookproto.Response
	if err := json.Unmarshal(data, &out); err != nil {
		return hookproto.Response{}, fmt.Errorf("%s@%d: the answer is not JSON: %w", r.connector, r.major, err)
	}
	if err := r.valid(req, op, a, out); err != nil {
		return hookproto.Response{}, err
	}
	return out, nil
}

// descriptor returns the connector's descriptor, read once per URL. A read
// that fails is retryable and not remembered.
func (r *remote) descriptor(ctx context.Context) (hookproto.Descriptor, error) {
	s := r.c.shared
	s.mu.Lock()
	d, ok := s.descriptors[r.conn.URL]
	s.mu.Unlock()
	if ok {
		return d, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.conn.URL+hookproto.DescriptorPath, nil)
	if err != nil {
		return d, fmt.Errorf("%w: the URL for %s@%d: %v", ErrConnectorUnconfigured, r.connector, r.major, err)
	}
	res, err := r.c.http.Do(req)
	if err != nil {
		return d, fmt.Errorf("%s@%d: read the descriptor: %w", r.connector, r.major, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return d, fmt.Errorf("%s@%d: read the descriptor: %s", r.connector, r.major, res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(&d); err != nil {
		return d, fmt.Errorf("%s@%d: the descriptor is not JSON: %w", r.connector, r.major, err)
	}
	s.mu.Lock()
	s.descriptors[r.conn.URL] = d
	s.mu.Unlock()
	return d, nil
}

// forget drops a URL's descriptor: the next call reads it again (a new
// major was deployed behind the same URL).
func (r *remote) forget() {
	r.c.shared.mu.Lock()
	delete(r.c.shared.descriptors, r.conn.URL)
	r.c.shared.mu.Unlock()
}

// matches checks the descriptor against the catalog's declaration of the
// operation, and returns the operation.
func (r *remote) matches(d hookproto.Descriptor, a action, operation string) (hookproto.Operation, error) {
	mismatch := func(format string, args ...any) (hookproto.Operation, error) {
		return hookproto.Operation{}, fmt.Errorf("%w: %s@%d at %s: %s", ErrConnectorMismatch, r.connector, r.major, r.conn.URL,
			fmt.Sprintf(format, args...))
	}
	if d.Connector != r.connector {
		return mismatch("it is connector %q", d.Connector)
	}
	if major, err := hookproto.Major(d.Version); err != nil || major != r.major {
		return mismatch("it is version %q", d.Version)
	}
	if !slices.Contains(d.SupportedProtocolVersions, hookproto.Protocol) {
		return mismatch("it speaks %v, not %s", d.SupportedProtocolVersions, hookproto.Protocol)
	}
	op, ok := d.Operation(operation)
	if !ok {
		return mismatch("it has no operation %s", operation)
	}
	for _, k := range a.kinds {
		if !slices.Contains(op.Kinds, k) {
			return mismatch("%s is not a %s", operation, k)
		}
	}
	if got := slices.Sorted(maps.Keys(op.InputSchema.Properties)); !slices.Equal(got, slices.Sorted(slices.Values(a.inputs))) {
		return mismatch("%s takes %v, declared %v", operation, got, a.inputs)
	}
	var outputs []string
	if op.OutputSchema != nil {
		outputs = slices.Sorted(maps.Keys(op.OutputSchema.Properties))
	}
	if !slices.Equal(outputs, slices.Sorted(slices.Values(a.outputs))) {
		return mismatch("%s returns %v, declared %v", operation, outputs, a.outputs)
	}
	return op, nil
}

// failure classes a non-200 answer: by its status, unless its problem says
// whether to retry.
func (r *remote) failure(res *http.Response, data []byte) error {
	var prob hookproto.Problem
	if strings.HasPrefix(res.Header.Get("Content-Type"), hookproto.ProblemContentType) {
		_ = json.Unmarshal(data, &prob)
	}
	msg := fmt.Sprintf("%s@%d: %s", r.connector, r.major, res.Status)
	if prob.Title != "" {
		msg += ": " + prob.Title
	}
	if prob.Retryable != nil {
		if *prob.Retryable {
			return errors.New(msg)
		}
		return permanent("%s", msg)
	}
	switch code := res.StatusCode; {
	case code >= 300 && code < 400:
		return permanent("%s: a redirect is never followed", msg)
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		return fmt.Errorf("%w: %s", ErrConnectorUnauthorized, msg)
	case code == http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %s", ErrKeyConflict, msg)
	case code == http.StatusConflict, code == http.StatusTooManyRequests, code >= 500:
		return errors.New(msg) // in flight, throttled or down: retry within the budget
	default:
		return permanent("%s", msg) // 400, 404, 410 and the rest of 4xx
	}
}

// valid checks an answer against the request, the descriptor and the
// declaration.
func (r *remote) valid(req hookproto.Request, op hookproto.Operation, a action, res hookproto.Response) error {
	name := fmt.Sprintf("%s@%d %s", r.connector, r.major, req.Hook.Operation)
	if res.Protocol != hookproto.Protocol {
		return permanent("%s answered protocol %q", name, res.Protocol)
	}
	if res.RequestID != req.RequestID {
		return permanent("%s answered request %q, not %q", name, res.RequestID, req.RequestID)
	}
	if res.ConnectorVersion != "" {
		if major, err := hookproto.Major(res.ConnectorVersion); err != nil || major != r.major {
			r.forget()
			return fmt.Errorf("%w: %s answered as version %q", ErrConnectorMismatch, name, res.ConnectorVersion)
		}
	}
	valid := map[hookproto.Kind][]hookproto.Outcome{
		hookproto.KindCheck:  {hookproto.OutcomeAllow, hookproto.OutcomeDeny, hookproto.OutcomeCorrect},
		hookproto.KindEnrich: {hookproto.OutcomeOK},
		hookproto.KindAction: {hookproto.OutcomeOK},
	}
	if !slices.Contains(valid[req.Kind], res.Outcome) {
		return permanent("%s answered %q to a %s", name, res.Outcome, req.Kind)
	}
	for k := range res.Patch {
		if !slices.Contains(a.outputs, k) {
			return permanent("%s answered an undeclared output %q", name, k)
		}
	}
	if res.Outcome == hookproto.OutcomeDeny {
		if res.Reason == nil || res.Reason.Code == "" {
			return fmt.Errorf("%w: %s denied without a reason", ErrConnectorMismatch, name)
		}
		if !slices.Contains(op.Errors, res.Reason.Code) {
			return fmt.Errorf("%w: %s denied with %q, which its descriptor does not declare", ErrConnectorMismatch, name, res.Reason.Code)
		}
		if res.Reason.Message != "" {
			// Logged, never stored: a message is free text.
			slog.Info("hook denied", "connector", r.connector, "operation", req.Hook.Operation,
				"reason", res.Reason.Code, "message", res.Reason.Message)
		}
	}
	return nil
}

// --- the connectors file ---

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
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var file struct {
		Connectors map[string]struct {
			URL       string `yaml:"url"`
			SecretEnv string `yaml:"secret_env"`
			Ref       string `yaml:"ref"`
		} `yaml:"connectors"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // a typo is an error, never ignored
	if err := dec.Decode(&file); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	cfg := Config{Connectors: map[string]Connection{}}
	for key, e := range file.Connectors {
		fail := func(format string, args ...any) (Config, error) {
			return Config{}, fmt.Errorf("%s: %s: %s", path, key, fmt.Sprintf(format, args...))
		}
		if _, _, ok := parseConnectorAtMajor(key); !ok {
			return fail("not <connector>@<major> (hr@1)")
		}
		u, err := url.Parse(e.URL)
		if e.URL == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fail("url %q: want an http or https URL", e.URL)
		}
		if e.SecretEnv == "" {
			return fail("no secret_env")
		}
		secret := getenv(e.SecretEnv)
		if secret == "" {
			return fail("secret_env %s is not set", e.SecretEnv)
		}
		if _, err := hookproto.ParseSecret(secret); err != nil {
			return fail("secret_env %s: %v", e.SecretEnv, err)
		}
		ref := e.Ref
		if ref == "" {
			ref = key
		}
		cfg.Connectors[key] = Connection{URL: strings.TrimSuffix(e.URL, "/"), Secret: secret, Ref: ref}
	}
	return cfg, nil
}

// configFile is a connectors file the worker follows.
type configFile struct {
	path string

	mu      sync.Mutex
	modTime time.Time
	size    int64
	conns   map[string]Connection // the last good file's
}

// WithConfigFile returns connectors configured by the connectors file at
// path. The file is read now (an error is returned) and again on any call
// after its modification time changes; a later read that fails keeps the
// last good configuration and logs the error. Secrets are read from the
// environment.
func (c *Connectors) WithConfigFile(path string) (*Connectors, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	cfg, err := LoadConfig(path, os.Getenv)
	if err != nil {
		return nil, err
	}
	with := *c
	with.file = &configFile{path: path, modTime: info.ModTime(), size: info.Size(), conns: cfg.Connectors}
	return &with, nil
}

// connection returns the file's connection for connector@major, after
// reading the file again if it changed.
func (f *configFile) connection(connectorAtMajor string) (Connection, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if info, err := os.Stat(f.path); err == nil && (!info.ModTime().Equal(f.modTime) || info.Size() != f.size) {
		f.modTime, f.size = info.ModTime(), info.Size() // seen: a bad file is logged once, not on every call
		if cfg, err := LoadConfig(f.path, os.Getenv); err != nil {
			slog.Error("connectors file: keeping the last good one", "error", err)
		} else {
			f.conns = cfg.Connectors
			slog.Info("connectors file read again", "path", f.path, "connectors", slices.Sorted(maps.Keys(cfg.Connectors)))
		}
	}
	conn, ok := f.conns[connectorAtMajor]
	return conn, ok
}
