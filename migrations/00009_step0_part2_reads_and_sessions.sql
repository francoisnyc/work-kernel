-- +goose Up
-- Step 0, part 2 (docs/design.md, "Public API (v1)"): new reads and agent
-- sessions in capability mode.

-- An Item's create time and creator are kept on the row, so reads need no
-- history. They are event 1's: its time, its actor, and the user that actor
-- acted for. Backfilled from event 1 for every existing Item.
alter table items
    add column created_at           timestamptz,
    add column created_by           text,
    add column created_on_behalf_of text;
update items i
   set created_at = e.created_at, created_by = e.actor, created_on_behalf_of = e.on_behalf_of
  from item_events e
 where e.item_id = i.id and e.item_seq = 1;
alter table items
    alter column created_at set not null,
    alter column created_by set not null;

-- Reads page newest first on (created_at, id). Filters on Type and state,
-- a container's children, and the top level each get an index that serves
-- that order, so a page is read without sorting the namespace.
create index items_namespace_type_state on items (namespace_id, type_id, state, created_at desc, id desc);
create index items_container_created on items (container_id, created_at desc, id desc);
create index items_top_level on items (namespace_id, created_at desc, id desc) where container_id is null;
drop index items_container; -- the index above serves every query it served

-- Sessions belong to one namespace and carry their capabilities: the
-- container Items they may touch (the nil UUID is the whole namespace) and
-- their powers, stored as given.
alter table agent_sessions
    add column namespace_id text   not null default 'default',
    add column scopes       uuid[] not null default '{}',
    add column powers       text[] not null default '{}';

-- +goose Down
alter table agent_sessions drop column powers, drop column scopes, drop column namespace_id;
create index items_container on items (container_id);
drop index items_top_level;
drop index items_container_created;
drop index items_namespace_type_state;
alter table items drop column created_on_behalf_of, drop column created_by, drop column created_at;
