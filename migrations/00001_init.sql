-- +goose Up
-- First-cut schema from docs/design.md.
-- One addition: idempotency.request_hash, so a key reused with a different
-- request can be rejected instead of silently replayed.

create table types (
    id         text,
    version    int,
    definition jsonb,
    primary key (id, version)
);

create table scopes (
    id        uuid primary key,
    parent_id uuid references scopes,
    narrows   boolean not null
);

create table scope_readers (
    scope_id  uuid references scopes,
    principal text,
    primary key (scope_id, principal)
);

create table grants (
    scope_id   uuid references scopes,
    principal  text,
    granted_by text,
    created_at timestamptz not null default now(),
    primary key (scope_id, principal)
);

create table items (
    id           uuid primary key,
    type_id      text not null,
    type_version int not null,
    container_id uuid references items,
    scope_id     uuid references scopes,
    state        text not null,
    fields       jsonb not null default '{}',
    item_seq     bigint not null default 0,
    updated_at   timestamptz not null default now()
);

create table item_events (
    item_id      uuid references items,
    item_seq     bigint,
    commit_seq   bigserial,
    actor        text not null,
    on_behalf_of text,
    op           text not null,
    payload      jsonb,
    created_at   timestamptz not null default now(),
    primary key (item_id, item_seq)
);

create table outbox (
    item_id      uuid,
    item_seq     bigint,
    delivered_at timestamptz,
    primary key (item_id, item_seq)
);

create table idempotency (
    key          text primary key,
    request_hash text not null,
    result       jsonb not null,
    created_at   timestamptz not null default now()
);

-- +goose Down
drop table idempotency;
drop table outbox;
drop table item_events;
drop table items;
drop table grants;
drop table scope_readers;
drop table scopes;
drop table types;
