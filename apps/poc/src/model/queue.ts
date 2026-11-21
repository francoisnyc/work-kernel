// The service desk's queue: one ListItems of a desk per view.
import type { MessageInitShape } from "@bufbuild/protobuf";
import { StateCategory, type Item, type ListItemsRequestSchema } from "@gen/kernel/v1/kernel_pb";
import { typeIdOf, versionOf } from "./labels";
import type { BreachedIndex } from "./sla";

export type QueueView = "open" | "unassigned" | "mine" | "waiting" | "breached";
export type QueueSort = "resolution" | "first_response";

export const queueViews: { view: QueueView; label: string }[] = [
  { view: "open", label: "All open" },
  { view: "unassigned", label: "Unassigned" },
  { view: "mine", label: "Mine" },
  { view: "waiting", label: "Waiting for customer" },
  { view: "breached", label: "Breached" },
];

const OPEN = [StateCategory.TRIAGE, StateCategory.ACTIVE, StateCategory.WAITING];

// The request for one view of a desk's incidents, sorted by a due instant
// (soonest first; none last). "breached" reads the open incidents: the
// breached ones are picked out with the goals (inView).
export function queueRequest(
  desk: string,
  ns: string,
  view: QueueView,
  sort: QueueSort,
  pageToken?: string,
): MessageInitShape<typeof ListItemsRequestSchema> {
  const types = [`${ns}/types/incident`];
  const filter: MessageInitShape<typeof ListItemsRequestSchema>["filter"] =
    view === "waiting"
      ? { types, states: ["waiting_for_customer"] }
      : view === "unassigned"
        ? { types, categories: OPEN, fields: [{ field: "f_assignee", empty: true }] }
        : view === "mine"
          ? { types, categories: OPEN, fields: [{ field: "f_assignee", eq: { kind: { case: "stringValue", value: "me" } } }] }
          : { types, categories: OPEN };
  return {
    parent: desk,
    filter,
    orderBy: sort === "resolution" ? "f_due_resolution" : "f_due_first_response",
    pageSize: 50,
    pageToken: pageToken ?? "",
  };
}

// What a view shows of a page: the apps' incident version only (incident@2;
// older ones are left to the admin UI), and for "breached" only the
// incidents with a breached goal.
export function inView(items: Item[], view: QueueView, breached: BreachedIndex): Item[] {
  return items
    .filter((i) => typeIdOf(i.type) === "incident" && versionOf(i.type) === 2)
    .filter((i) => view !== "breached" || breached.has(i.name, "first_response") || breached.has(i.name, "resolution"));
}

// A count for a view's tab: "12", or "50+" when there is another page.
export function countLabel(shown: number, more: boolean): string {
  return more ? `${shown}+` : String(shown);
}
