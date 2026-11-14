-- +goose Up
-- Step 6 (docs/design.md, "Holds"): a checked transition is asked for, held
-- while its check runs, then resolved. One row per request.

create table item_holds (
    id                      uuid primary key,
    item_id                 uuid not null references items,
    hook                    text not null,   -- the check hook's ID in the Item's Type version
    from_state              text not null,
    to_state                text not null,
    requested_seq           bigint not null, -- the transition_requested event
    -- The requester, checked again when the hold resolves. Opaque IDs only.
    requester               text not null,
    requester_on_behalf_of  text,
    requester_agent_session text,
    status                  text not null default 'open'
                            check (status in ('open', 'allowed', 'denied', 'error', 'canceled')),
    reason                  text,            -- a code, never free text
    resolved_seq            bigint,          -- the event that closed the hold
    created_at              timestamptz not null default now(),
    resolved_at             timestamptz,
    foreign key (item_id, requested_seq) references item_events
);

-- At most one open hold per Item: a second request is refused as held.
create unique index item_holds_one_open on item_holds (item_id) where status = 'open';
create index item_holds_item on item_holds (item_id, requested_seq);

-- +goose Down
drop table item_holds;
