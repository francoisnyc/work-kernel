// The service desk's queue: one ListItems of a desk per view.
import type { MessageInitShape } from "@bufbuild/protobuf";
import type { Item, ListItemsRequestSchema } from "@gen/kernel/v1/kernel_pb";
import type { BreachedIndex } from "./sla";
import { todo } from "./todo";

export type QueueView = "open" | "unassigned" | "mine" | "waiting" | "breached";
export type QueueSort = "resolution" | "first_response";

export const queueViews: { view: QueueView; label: string }[] = [];

// The request for one view of a desk's incidents, sorted by a due instant
// (soonest first; none last). "breached" reads the open incidents: the
// breached ones are picked out with the goals (inView).
export function queueRequest(
  _desk: string,
  _ns: string,
  _view: QueueView,
  _sort: QueueSort,
  _pageToken?: string,
): MessageInitShape<typeof ListItemsRequestSchema> {
  return todo();
}

// What a view shows of a page: the apps' incident version only (incident@2;
// older ones are left to the admin UI), and for "breached" only the
// incidents with a breached goal.
export function inView(_items: Item[], _view: QueueView, _breached: BreachedIndex): Item[] {
  return todo();
}

// A count for a view's tab: "12", or "50+" when there is another page.
export function countLabel(_shown: number, _more: boolean): string {
  return todo();
}
