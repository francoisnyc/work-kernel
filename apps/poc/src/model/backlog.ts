// The tracker's backlog: a space's open issues that are in no open sprint.
// The API has no "has no sprint link" filter and search results carry no
// links: the sprints' own link searches say which issues they hold, and
// the rest is worked out here.
import type { MessageInitShape } from "@bufbuild/protobuf";
import type { Item, ItemFilterSchema } from "@gen/kernel/v1/kernel_pb";
import { todo } from "./todo";

export type IssueType = "story" | "bug" | "task";
export type QuickFilters = { mine: boolean; types: IssueType[]; priority: string; label: string };
export const noFilters: QuickFilters = { mine: false, types: [], priority: "", label: "" };

// The filter for a space's open issues (todo and active categories) under
// the quick filters: no type chosen means all three.
export function issueFilter(_ns: string, _quick: QuickFilters): MessageInitShape<typeof ItemFilterSchema> {
  return todo();
}

// The issues not held by an open sprint, in the order given.
export function backlogOnly(_issues: Item[], _inOpenSprints: ReadonlySet<string>): Item[] {
  return todo();
}

// A word search cannot be held to one container: what it found, kept to
// one space (pages can come back short).
export function inContainer(_items: Item[], _container: string): Item[] {
  return todo();
}
