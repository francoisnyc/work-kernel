-- +goose Up
-- Jira part 2, step 4a: customers.
--
-- 1. The idempotency key of an agent belongs to the agent AND the person it
--    acts for: (namespace_id, actor, effective_user, key). Before, an agent
--    acting for kim and then for lee with one key had lee's write refused
--    as a reuse of kim's (the same for an agent acting for two staff
--    users). effective_user is the person an agent acts for; for everyone
--    else (people, customers, the kernel's own actors) it is the actor.
--    Existing rows keep their answers: a row of an agent takes the person
--    its stored result's event was written for, any other row its actor,
--    which is what a retry of that same request now looks up.
alter table idempotency add column effective_user text;
update idempotency set effective_user = actor;
update idempotency i
   set effective_user = e.on_behalf_of
  from item_events e
 where i.actor like 'agent:%'
   and e.item_id = (i.result ->> 'item_id')::uuid
   and e.item_seq = (i.result ->> 'item_seq')::bigint
   and e.on_behalf_of is not null;
alter table idempotency alter column effective_user set not null;
alter table idempotency drop constraint idempotency_pkey;
alter table idempotency add primary key (namespace_id, actor, effective_user, key);

-- 2. A customer's Search and List start from their own requests: Items by
--    their effective creator (policy.CustomerRelation). Partial: only
--    Items made by or for a customer are in it, so staff writes do not pay
--    for it.
create index items_requester on items (namespace_id, (coalesce(created_on_behalf_of, created_by)), created_at desc, id desc)
    where coalesce(created_on_behalf_of, created_by) like 'customer:%';

-- +goose Down
drop index items_requester;
alter table idempotency drop constraint idempotency_pkey;
alter table idempotency add primary key (namespace_id, actor, key);
alter table idempotency drop column effective_user;
