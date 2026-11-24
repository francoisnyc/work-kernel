// Package store is what Temporal should not hold: a table of requests the
// inbox and search read (a projection the workflows write), and an audit
// trail. The workflow stays the source of truth; nothing here decides.
package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const schema = `
create table if not exists requests (
  id           text primary key,
  kind         text not null,
  requester    text not null,
  state        text not null,
  waiting_on   text[] not null default '{}',   -- principals, or role:<name>
  readers      text[] not null default '{}',   -- principals, or role:<name>
  claimed_by   text not null default '',
  prepare_open boolean not null default false, -- the agent may still write its preparation
  data         jsonb not null,                 -- the workflow's state, as it last reported it
  updated_at   timestamptz not null default now()
);
create table if not exists audit (
  request_id text not null,
  seq        int not null,                     -- the workflow's own counter: a retried write lands once
  at         timestamptz not null default now(),
  actor      text not null,
  action     text not null,
  detail     jsonb not null default '{}',
  primary key (request_id, seq)
);`

// Row is one request as the inbox sees it.
type Row struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Requester   string          `json:"requester"`
	State       string          `json:"state"`
	WaitingOn   []string        `json:"waiting_on"`
	Readers     []string        `json:"readers"`
	ClaimedBy   string          `json:"claimed_by,omitempty"`
	PrepareOpen bool            `json:"prepare_open"`
	Data        json.RawMessage `json:"data"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// Event is one line of the audit trail.
type Event struct {
	Seq    int            `json:"seq"`
	At     time.Time      `json:"at"`
	Actor  string         `json:"actor"`
	Action string         `json:"action"`
	Detail map[string]any `json:"detail,omitempty"`
}

type Store struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Record writes the request's new picture and, if ev is set, one audit
// line, in one transaction. Safe to retry.
func (s *Store) Record(ctx context.Context, row Row, ev *Event) error {
	// pgx.BeginFunc commits if the function returns nil and rolls back
	// otherwise: Go's stand-in for try-with-resources around a transaction.
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			insert into requests (id, kind, requester, state, waiting_on, readers, claimed_by, prepare_open, data, updated_at)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9, now())
			on conflict (id) do update set state=$4, waiting_on=$5, readers=$6, claimed_by=$7, prepare_open=$8, data=$9, updated_at=now()`,
			row.ID, row.Kind, row.Requester, row.State, row.WaitingOn, row.Readers, row.ClaimedBy, row.PrepareOpen, row.Data)
		if err != nil || ev == nil {
			return err
		}
		detail, err := json.Marshal(ev.Detail)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into audit (request_id, seq, actor, action, detail) values ($1,$2,$3,$4,$5)
			on conflict do nothing`, row.ID, ev.Seq, ev.Actor, ev.Action, detail)
		return err
	})
}

const rowCols = `id, kind, requester, state, waiting_on, readers, claimed_by, prepare_open, data, updated_at`

func scan(rows pgx.Rows) ([]Row, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Row, error) {
		var x Row
		err := r.Scan(&x.ID, &x.Kind, &x.Requester, &x.State, &x.WaitingOn, &x.Readers, &x.ClaimedBy, &x.PrepareOpen, &x.Data, &x.UpdatedAt)
		return x, err
	})
}

// Get returns one request, or pgx.ErrNoRows.
func (s *Store) Get(ctx context.Context, id string) (Row, error) {
	rows, err := s.pool.Query(ctx, `select `+rowCols+` from requests where id=$1`, id)
	if err != nil {
		return Row{}, err
	}
	all, err := scan(rows)
	if err != nil {
		return Row{}, err
	}
	if len(all) == 0 {
		return Row{}, pgx.ErrNoRows
	}
	return all[0], nil
}

// WaitingOn lists the requests waiting on any of these names (a principal
// and its role:<name> entries), oldest first: the inbox, for every kind.
func (s *Store) WaitingOn(ctx context.Context, names []string) ([]Row, error) {
	rows, err := s.pool.Query(ctx, `select `+rowCols+` from requests where waiting_on && $1 order by updated_at, id`, names)
	if err != nil {
		return nil, err
	}
	return scan(rows)
}

// ToPrepare lists the requests still open for the agent's preparation that have none yet.
func (s *Store) ToPrepare(ctx context.Context) ([]Row, error) {
	rows, err := s.pool.Query(ctx, `select `+rowCols+` from requests
		where prepare_open and (data->'preparation' is null or data->'preparation' = 'null') order by updated_at, id`)
	if err != nil {
		return nil, err
	}
	return scan(rows)
}

// History returns a request's audit trail, in order.
func (s *Store) History(ctx context.Context, id string) ([]Event, error) {
	rows, err := s.pool.Query(ctx, `select seq, at, actor, action, detail from audit where request_id=$1 order by seq`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
		var e Event
		var detail []byte
		if err := r.Scan(&e.Seq, &e.At, &e.Actor, &e.Action, &detail); err != nil {
			return e, err
		}
		return e, json.Unmarshal(detail, &e.Detail)
	})
}
