-- +goose Up
-- Step 4: every Item has a scope, roles are server-side rows, and string
-- field values are searchable. See docs/design.md (Policy, Schema).

-- A scope belongs to a namespace. Each namespace has one root scope: no
-- parent, narrows = false, readable by everyone in the namespace.
alter table scopes add column namespace_id text not null default 'default';
create unique index scopes_root on scopes (namespace_id) where parent_id is null;
create index scopes_parent on scopes (parent_id);

insert into scopes (id, namespace_id, parent_id, narrows)
select gen_random_uuid(), n.namespace_id, null, false
from (select 'default' as namespace_id union select namespace_id from items) n;

-- Backfill: Items created before Step 4 were visible to everyone, which is
-- what the root scope means. After this there is no "null means everyone".
update items i set scope_id = s.id
from scopes s
where i.scope_id is null and s.namespace_id = i.namespace_id and s.parent_id is null;
alter table items alter column scope_id set not null;

-- Who holds which role. Read on every write that needs it; a request never
-- claims a role for itself.
create table principal_roles (
    namespace_id text not null default 'default',
    principal    text not null,
    role         text not null,
    primary key (namespace_id, principal, role)
);

-- Full-text search over the string values in fields. The column is generated,
-- so it always matches the row: there is no index to fall behind.
-- 'simple' does no stemming, which suits a spike with no language setting.
alter table items add column search tsvector
    generated always as (jsonb_to_tsvector('simple', fields, '["string"]')) stored;
create index items_search on items using gin (search);

-- Search and visibility checks filter on the scope first.
create index items_namespace_scope on items (namespace_id, scope_id);

-- +goose Down
drop index items_namespace_scope;
drop index items_search;
alter table items drop column search;
drop table principal_roles;
alter table items alter column scope_id drop not null;
update items i set scope_id = null
from scopes s where s.id = i.scope_id and s.parent_id is null;
delete from scopes where parent_id is null;
drop index scopes_parent;
drop index scopes_root;
alter table scopes drop column namespace_id;
