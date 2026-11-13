-- +goose Up
-- Schema follow-ups from the Step 1 review; see docs/design.md (Write path, Schema).

-- One default namespace. Multi-tenancy itself stays out of scope.
alter table items add column namespace_id text not null default 'default';

-- Attribution columns. Callers fill them as agents and hooks appear.
alter table item_events
    add column agent_session    text,
    add column originating_hook text,
    add column causation_id     text,
    add column correlation_id   text,
    add column hop              int not null default 0;

-- commit_seq now comes from one locked counter row, so values become visible
-- in commit order. A sequence hands out numbers at insert time, not at commit.
alter table item_events alter column commit_seq drop default;
drop sequence item_events_commit_seq_seq;
alter table item_events add constraint item_events_commit_seq_key unique (commit_seq);

create table commit_counter (
    id       boolean primary key default true check (id), -- at most one row
    last_seq bigint not null
);
insert into commit_counter (last_seq) select coalesce(max(commit_seq), 0) from item_events;

-- Field values for each event, kept out of the permanent log so they can be erased.
create table item_event_values (
    item_id      uuid,
    item_seq     bigint,
    field_values jsonb not null,
    primary key (item_id, item_seq),
    foreign key (item_id, item_seq) references item_events
);

-- Idempotency keys are scoped by (namespace_id, actor, key).
alter table idempotency
    add column namespace_id text not null default 'default',
    add column actor        text;
update idempotency i
   set actor = e.actor
  from item_events e
 where e.item_id = (i.result ->> 'item_id')::uuid
   and e.item_seq = (i.result ->> 'item_seq')::bigint;
alter table idempotency alter column actor set not null;
alter table idempotency drop constraint idempotency_pkey;
alter table idempotency add primary key (namespace_id, actor, key);

create index outbox_pending on outbox (item_id, item_seq) where delivered_at is null;
create index items_container on items (container_id);

-- +goose Down
drop index items_container;
drop index outbox_pending;

alter table idempotency drop constraint idempotency_pkey;
alter table idempotency drop column actor, drop column namespace_id;
alter table idempotency add primary key (key);

drop table item_event_values;

create sequence item_events_commit_seq_seq owned by item_events.commit_seq;
select setval('item_events_commit_seq_seq', greatest((select last_seq from commit_counter), 1));
alter table item_events alter column commit_seq set default nextval('item_events_commit_seq_seq');
alter table item_events drop constraint item_events_commit_seq_key;
drop table commit_counter;

alter table item_events
    drop column hop,
    drop column correlation_id,
    drop column causation_id,
    drop column originating_hook,
    drop column agent_session;

alter table items drop column namespace_id;
