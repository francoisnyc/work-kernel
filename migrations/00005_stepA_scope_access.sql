-- +goose Up
-- Step A (docs/design.md, Policy): a scope node per container, scopes.open
-- and scope_access.

-- A scope node belongs to one Item: the Item that was narrowed, or the
-- container whose first child created it. Null for the root. Unique, so two
-- first children created at once cannot make two nodes.
alter table scopes add column item_id uuid references items;
create unique index scopes_item on scopes (item_id);
-- Backfill: a node belongs to the topmost Item that uses it (Step 4 copied a
-- narrowed container's scope onto its children).
update scopes s set item_id = i.id
from items i
where i.scope_id = s.id and s.parent_id is not null
  and (i.container_id is null
       or (select c.scope_id from items c where c.id = i.container_id) <> s.id);

-- open: no narrowing ancestor, the node itself included. Everyone in the
-- namespace reads an open scope.
alter table scopes add column open boolean;
with recursive t as (
    select id, not narrows as open from scopes where parent_id is null
    union all
    select s.id, t.open and not s.narrows from scopes s join t on s.parent_id = t.id
)
update scopes s set open = t.open from t where t.id = s.id;
alter table scopes alter column open set not null;

-- scope_access: the effective readers of every scope that is not open, i.e.
-- the readers of every narrowing ancestor, intersected. Kept up to date in the
-- same transaction as every reader or policy change.
create table scope_access (
    scope_id  uuid references scopes,
    principal text,
    primary key (scope_id, principal)
);
with recursive t as (
    select id, null::text[] as readers from scopes where parent_id is null
    union all
    select s.id,
           case
               when not s.narrows then t.readers
               when t.readers is null then
                   array(select r.principal from scope_readers r where r.scope_id = s.id)
               else
                   array(select unnest(t.readers)
                         intersect
                         select r.principal from scope_readers r where r.scope_id = s.id)
           end
    from scopes s join t on s.parent_id = t.id
)
insert into scope_access (scope_id, principal)
select id, unnest(readers) from t where readers is not null;

-- +goose Down
drop table scope_access;
alter table scopes drop column open;
drop index scopes_item;
alter table scopes drop column item_id;
