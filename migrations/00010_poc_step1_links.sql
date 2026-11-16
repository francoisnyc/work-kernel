-- +goose Up
-- Proof of concept, Step 1 (docs/design.md, "Links, containment and
-- people"): typed links between Items. A link belongs to its source: adding
-- or removing one is an event on the source only.
--
-- Cardinality is enforced here, not by check-then-write: src_single and
-- dst_single are snapshots of the source Type's declaration at write time
-- (max: one, inverse_max: one), and the partial unique indexes allow one
-- such link per source, or per target. A later Type version that drops a
-- link type or changes its cardinality leaves existing rows as they were.
create table links (
    namespace_id   text        not null,
    source_id      uuid        not null references items,
    source_type_id text        not null,
    link_type      text        not null,  -- the declared name
    target_id      uuid        not null references items,
    src_single     boolean     not null,
    dst_single     boolean     not null,
    created_at     timestamptz not null default now(),
    primary key (source_id, link_type, target_id)
);
create unique index links_src_single on links (source_id, link_type) where src_single;
create unique index links_dst_single on links (target_id, source_type_id, link_type) where dst_single;
-- Incoming links: an Item's inverse view, guards and the "linked to" filter.
create index links_target on links (target_id, link_type, source_id);
-- Get shows each direction in a fixed order (created_at, the declared name,
-- the other end's ID) and stops at 201 rows: these walk it in that order,
-- so a hub's Get reads one page, not all its links.
create index links_source_order on links (source_id, created_at, link_type, target_id);
create index links_target_order on links (target_id, created_at, link_type, source_id);

-- +goose Down
drop table links;
