-- +goose Up
-- Step A (docs/design.md, Policy): every scope points to its nearest narrowing
-- scope, and only narrowing scopes hold scope_access rows. Copying the
-- effective readers onto every inheriting scope made narrowing a container
-- with 1,000 descendant scopes to 50 readers write 50,050 rows.

-- policy_scope_id: the nearest narrowing scope at or above this one; null when
-- nothing narrows (everyone in the namespace reads it). Replaces scopes.open.
-- No foreign key: it is derived data, rewritten by every policy change and
-- checked against a naive evaluator by the property test. A foreign key would
-- make writing a pointer to an ancestor take a share lock on the ancestor's
-- row, which a concurrent change to that ancestor holds FOR UPDATE while it
-- waits for this subtree: a deadlock.
alter table scopes add column policy_scope_id uuid;
with recursive t as (
    select id, case when narrows then id end as ptr from scopes where parent_id is null
    union all
    select s.id, case when s.narrows then s.id else t.ptr end
    from scopes s join t on s.parent_id = t.id
)
update scopes s set policy_scope_id = t.ptr from t where t.id = s.id;
create index scopes_policy on scopes (policy_scope_id);

-- Inheriting scopes keep no rows of their own any more.
delete from scope_access a using scopes s where s.id = a.scope_id and not s.narrows;
alter table scopes drop column open;

-- +goose Down
alter table scopes add column open boolean;
update scopes set open = policy_scope_id is null;
alter table scopes alter column open set not null;
insert into scope_access (scope_id, principal)
select s.id, a.principal from scopes s join scope_access a on a.scope_id = s.policy_scope_id
where not s.narrows;
drop index scopes_policy;
alter table scopes drop column policy_scope_id;
