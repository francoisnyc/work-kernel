-- +goose Up
-- Step A (docs/design.md, Write path): commit_seq from a sequence, each
-- event's transaction ID, and the idle-in-transaction timeout.

-- commit_seq comes from a plain sequence: no row lock, so writes no longer
-- queue behind each other. It names a write; it is not a cursor.
create sequence commit_seq owned by item_events.commit_seq;
select setval('commit_seq', greatest(last_seq, 1), last_seq > 0) from commit_counter;
alter table item_events alter column commit_seq set default nextval('commit_seq');
drop table commit_counter;

-- Each event records the transaction that wrote it. min_seq waits until that
-- ID is below pg_snapshot_xmin(pg_current_snapshot()): the write and every
-- older transaction have finished. Existing events get this migration's ID,
-- which is below the watermark as soon as the migration commits.
alter table item_events add column xid xid8 not null default pg_current_xact_id();

-- End transactions left idle, so a forgotten session cannot hold the
-- watermark back for everyone. ALTER DATABASE ... SET only applies to
-- sessions opened after it: connections already open keep the old value
-- until they reconnect.
-- +goose StatementBegin
do $$
begin
    execute format('alter database %I set idle_in_transaction_session_timeout = %L',
                   current_database(), '60s');
end
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
do $$
begin
    execute format('alter database %I reset idle_in_transaction_session_timeout', current_database());
end
$$;
-- +goose StatementEnd
alter table item_events drop column xid;
create table commit_counter (
    id       boolean primary key default true check (id),
    last_seq bigint not null
);
insert into commit_counter (last_seq) select coalesce(max(commit_seq), 0) from item_events;
alter table item_events alter column commit_seq drop default;
drop sequence commit_seq;
