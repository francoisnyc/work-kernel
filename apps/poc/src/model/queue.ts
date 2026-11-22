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

// The incident versions the apps know: incident@2 and incident@3 (the
// hardening step: placed by desk agents, its goals on sla_goal@3), listed
// together. incident@1 is left to the admin UI.
export const INCIDENT_VERSIONS = [2, 3];

// What a view shows of a page: the apps' incident versions only, and for
// "breached" only the incidents with a breached goal.
export function inView(items: Item[], view: QueueView, breached: BreachedIndex): Item[] {
  return items
    .filter((i) => typeIdOf(i.type) === "incident" && INCIDENT_VERSIONS.includes(versionOf(i.type)))
    .filter((i) => view !== "breached" || breached.has(i.name, "first_response") || breached.has(i.name, "resolution"));
}

// A count for a view's tab: "12", or "50+" when there is another page.
export function countLabel(shown: number, more: boolean): string {
  return more ? `${shown}+` : String(shown);
}
