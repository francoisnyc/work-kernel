-- +goose Up
-- Track 1, item 5, step 3a: when an Item entered its current state.
--
-- state_entered_at is the time of the event that put the Item in the state
-- it is in: its create, or its latest transition (the same event
-- itemservice.StateEntry reads). The write that creates an Item or changes
-- its state sets it, in the same transaction. A field update, a link, a
-- migrate, a move of container and a hook's write do not touch it.
--
-- It is what "oldest waiting first" sorts by (order_by: waiting_since on
-- the waiting_on view). No index: the view reads its candidates through
-- items_namespace_type_state, as before, and sorts them.
--
-- Existing Items take the time of their latest create or transition event;
-- an Item with no such event (none should exist) takes its create time.
alter table items add column state_entered_at timestamptz;
update items i
   set state_entered_at = coalesce(
       (select e.created_at
          from item_events e
         where e.item_id = i.id and e.op in ('create', 'transition')
         order by e.item_seq desc
         limit 1),
       i.created_at);
alter table items alter column state_entered_at set not null;

-- +goose Down
alter table items drop column state_entered_at;
