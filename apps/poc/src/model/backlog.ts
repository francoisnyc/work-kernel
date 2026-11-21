// The tracker's backlog: a space's open issues that are in no open sprint.
// The API has no "has no sprint link" filter and search results carry no
// links: the sprints' own link searches say which issues they hold, and
// the rest is worked out here.
import type { MessageInitShape } from "@bufbuild/protobuf";
import { StateCategory, type Item, type ItemFilterSchema } from "@gen/kernel/v1/kernel_pb";

export type IssueType = "story" | "bug" | "task";
export type QuickFilters = { mine: boolean; types: IssueType[]; priority: string; label: string };
export const noFilters: QuickFilters = { mine: false, types: [], priority: "", label: "" };

const ALL: IssueType[] = ["story", "bug", "task"];
const str = (value: string) => ({ kind: { case: "stringValue" as const, value } });

// The filter for a space's open issues (todo and active categories) under
// the quick filters: no type chosen means all three.
export function issueFilter(ns: string, quick: QuickFilters): MessageInitShape<typeof ItemFilterSchema> {
  const types = (quick.types.length > 0 ? quick.types : ALL).map((t) => `${ns}/types/${t}`);
  const fields = [
    ...(quick.mine ? [{ field: "f_assignee", eq: str("me") }] : []),
    ...(quick.priority !== "" ? [{ field: "f_priority", eq: str(quick.priority) }] : []),
    ...(quick.label !== "" ? [{ field: "f_labels", eq: str(quick.label) }] : []),
  ];
  const filter: MessageInitShape<typeof ItemFilterSchema> = { types, categories: [StateCategory.TODO, StateCategory.ACTIVE] };
  if (fields.length > 0) filter.fields = fields;
  return filter;
}

// The issues not held by an open sprint, in the order given.
export function backlogOnly(issues: Item[], inOpenSprints: ReadonlySet<string>): Item[] {
  return issues.filter((i) => !inOpenSprints.has(i.name));
}

// A word search cannot be held to one container: what it found, kept to
// one space (pages can come back short).
export function inContainer(items: Item[], container: string): Item[] {
  return items.filter((i) => i.container === container);
}
