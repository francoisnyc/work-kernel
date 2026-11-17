-- +goose Up
-- Proof of concept, Step 2, part 2 (docs/design.md, "Link reactions"): a
-- link reaction adds to an int field. item_event_values keeps the result
-- (field_values is always the value after the event); field_deltas keeps
-- the amount added, so history shows both and an undo reverses exactly what
-- the reaction took. Null for every event that added nothing. It sits with
-- the values: erasing an event's values erases its deltas too.
alter table item_event_values add column field_deltas jsonb;

-- +goose Down
alter table item_event_values drop column field_deltas;
