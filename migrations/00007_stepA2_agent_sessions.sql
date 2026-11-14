-- +goose Up
-- Step A2 (docs/design.md, Policy): the agent write rule.

-- Sessions are minted by the item service (StartAgentSession) and bound to
-- one agent and the user it acts for (null when it acts for itself).
create table agent_sessions (
    session      text primary key,
    agent        text not null,
    on_behalf_of text,
    created_at   timestamptz not null default now()
);

-- Every narrowing scope an agent read in a session. No foreign key on
-- scope_id: a recorded scope may be removed later (Inherit on an Item with no
-- children); the record stays, no target is below it any more, and every
-- write in that session then needs approval. Conservative, never looser.
create table agent_session_reads (
    session  text references agent_sessions,
    scope_id uuid,
    primary key (session, scope_id)
);

-- Approvals by the session's user to write into one wider scope. Audited:
-- who approved, and when.
create table agent_session_approvals (
    session     text references agent_sessions,
    scope_id    uuid,
    approved_by text,
    created_at  timestamptz not null default now(),
    primary key (session, scope_id, approved_by)
);

-- +goose Down
drop table agent_session_approvals;
drop table agent_session_reads;
drop table agent_sessions;
