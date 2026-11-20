-- +goose Up
-- The etag step (docs/design.md, "Writes"): a second counter per Item, in
-- the same row and the same transaction as the write. item_seq counts
-- every event; content_seq counts the writes that changed something a
-- caller could overwrite (the state, a field a caller may write, a link,
-- the readers, the container). The public etag is derived from it, so a
-- person's save is no longer refused because the kernel recorded an
-- outcome or wrote a field only it writes.
--
-- Existing Items start at their item_seq: an etag read before this
-- migration was item_seq, and what was stale stays stale.
alter table items add column content_seq bigint not null default 0;
update items set content_seq = item_seq;

-- +goose Down
alter table items drop column content_seq;
