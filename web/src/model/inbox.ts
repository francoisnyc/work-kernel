// The Inbox page's model (track 1 item 5, step 4, PR 2): what the page
// asks the kernel and how it words the answer. Pure functions.
//
// The kernel decides who may act: the rows come from `waiting_on`, the
// counts from CountItems, the buttons from each Item's `moves`. Nothing
// here reads `allowed`, `except` or a claim to decide what a person may
// do. The one thing read from the Types is which role names appear in
// some `allowed`, to choose which of the caller's roles get a tab.
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import {
  FieldType,
  TransitionSchema,
  type CountItemsRequestSchema,
  type Item,
  type SearchItemsRequestSchema,
  type Transition,
  type Type,
} from "../gen/kernel/v1/kernel_pb";

// One tab: "Waiting on me", or a role's queue.
export type InboxTab = { key: string; label: string; waitingOn: string; role?: string };

export type InboxOrder = "oldest" | "newest";

// A page of the Inbox.
export const INBOX_PAGE = 50;

// What the page is showing: a tab, an order, an optional place (an Item's
// name; "" for anywhere).
export type InboxView = {
  parent: string;
  tab: InboxTab;
  order: InboxOrder;
  place: string;
  // A team tab's claimed rows ("Being handled", filter.claimed) instead of
  // its queue (Inbox PR 2b). stub (Phase B): not sent yet.
  claimed?: boolean;
};

const ME: InboxTab = { key: "me", label: "Waiting on me", waitingOn: "me" };

// "Waiting on me", then one tab per role the caller holds (WhoAmI) that
// some Type names in a transition's `allowed`, in byte order. A role no
// Type names can have no queue, so it gets no tab. This reads which names
// are written in the Types; it decides nothing about who may act.
export function inboxTabs(roles: readonly string[], types: Type[]): InboxTab[] {
  const named = new Set<string>();
  for (const type of types) {
    for (const t of type.transitions) {
      for (const a of t.allowed) if (a.startsWith("role:")) named.add(a.slice("role:".length));
    }
  }
  const held = [...new Set(roles)].filter((r) => named.has(r)).sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
  return [ME, ...held.map((r) => ({ key: `role:${r}`, label: r, waitingOn: `role:${r}`, role: r }))];
}

function filterOf(v: Pick<InboxView, "tab" | "place">) {
  return v.place === "" ? { waitingOn: v.tab.waitingOn } : { waitingOn: v.tab.waitingOn, in: v.place };
}

// The search a view sends: the tab's waiting_on, the order, a page of 50,
// the place if one is chosen. It names no Type, state or field.
export function inboxSearch(v: InboxView, page: { pageToken?: string; minSeq?: bigint } = {}): MessageInitShape<typeof SearchItemsRequestSchema> {
  return {
    parent: v.parent,
    pageSize: INBOX_PAGE,
    orderBy: v.order === "oldest" ? "waiting_since" : "waiting_since desc",
    filter: filterOf(v),
    ...(page.pageToken ? { pageToken: page.pageToken } : {}),
    ...(page.minSeq ? { minSeq: page.minSeq } : {}),
  };
}

// The count behind a tab's number: the same filter, no page, no order.
export function inboxCount(v: Omit<InboxView, "order">): MessageInitShape<typeof CountItemsRequestSchema> {
  return { parent: v.parent, filter: filterOf(v) };
}

// "12", or "50+" when the kernel says there are more; "" before the answer.
export function countLabel(c: { count: number; capped: boolean } | undefined): string {
  if (!c) return "";
  return c.capped ? `${c.count}+` : String(c.count);
}

// A row's buttons: exactly the moves the server listed for the caller
// (Item.moves), in its order. The Type is read only to show a move's form
// and rules; a move this copy of the Type does not have is still offered.
export function rowMoves(type: Type | undefined, item: Item): Transition[] {
  return item.moves.map(
    (to) =>
      type?.transitions.find((t) => t.fromState === item.state && t.toState === to) ??
      create(TransitionSchema, { fromState: item.state, toState: to }),
  );
}

// Why a row is in this tab, as far as the data says it directly:
//   - on a role's tab, the tab: "for your team (finance)";
//   - on Waiting on me, "sent back to you" when the Item's state says it
//     waits on its requester (GetType's waits_on_requester) and the caller
//     is its requester (askedBy);
//   - on Waiting on me, the principal fields that hold the caller, by ID:
//     "f_approver is you"; nothing when no field does (the kernel put the
//     Item in the view; the browser does not re-derive why).
export function whyYours(tab: InboxTab, type: Type | undefined, item: Item, principal: string): string {
  if (tab.role !== undefined) return `for your team (${tab.role})`;
  const state = type?.states.find((s) => s.id === item.state);
  if (state?.waitsOnRequester && askedBy(item) === principal) return "sent back to you";
  const ids = (type?.fields ?? [])
    .filter((f) => f.type === FieldType.PRINCIPAL && item.fields?.[f.id] === principal)
    .map((f) => f.id);
  return ids.length === 0 ? "" : `${ids.join(", ")} is you`;
}

// Who asked: the creator, or the user an agent created the Item for.
export function askedBy(item: Item): string {
  return item.creatorOnBehalfOf !== "" ? item.creatorOnBehalfOf : item.creator;
}

const units: [number, string][] = [
  [86_400, "d"],
  [3_600, "h"],
  [60, "min"],
  [1, "s"],
];

// "waiting 3 d": since the Item entered its state (Item.state_time), the
// largest unit that fits, rounded down. Clock time, not working time.
export function waitingFor(stateTime: Timestamp | undefined, now: Date): string {
  if (!stateTime) return "";
  const seconds = Math.floor((now.getTime() - timestampDate(stateTime).getTime()) / 1000);
  if (seconds < 1) return "just arrived";
  const [size, unit] = units.find(([s]) => seconds >= s)!;
  return `waiting ${Math.floor(seconds / size)} ${unit}`;
}

// --- The Inbox for people, PR 2b: rows, groups, team tabs. ---
// stub (Phase B): every function below answers nothing yet.

// A person's name for the page (devusers.personName), or the ID.
export type NameOf = (principal: string) => string;

// A role's ID made readable for its tab: "desk_agent" is "Desk agent".
export function roleLabel(role: string): string {
  return role;
}

// A state's label, or its ID when the Type gives none.
export function stateLabel(type: Type | undefined, state: string): string {
  return state;
}

// The row's main button and the rest (the "More moves" disclosure): the
// move marked primary when the server lists it, else the first it lists.
export function splitMoves(type: Type | undefined, item: Item): { main?: Transition; others: Transition[] } {
  return { others: rowMoves(type, item) };
}

// The Type's summary fields that hold a value, as "Label: value", in the
// summary's order; a principal is named.
export function summaryLine(type: Type | undefined, item: Item, nameOf: NameOf): string[] {
  return [];
}

// "Ana asks", or "You asked for this one".
export function askedLine(item: Item, principal: string, nameOf: NameOf): string {
  return "";
}

// "waiting 3 h"; "in hand 3 h" in a state with a claim.
export function waitedLine(type: Type | undefined, item: Item, now: Date): string {
  return "";
}

// When the Item is due in its state (the state's `due` field), and
// whether that has passed.
export type Due = { text: string; overdue: boolean };
export function dueOf(type: Type | undefined, item: Item, now: Date): Due | undefined {
  return undefined;
}

// Overdue first, then the rest, each in the order given.
export function groupRows(rows: Item[], typeOf: (name: string) => Type | undefined, now: Date): { overdue: Item[]; waiting: Item[] } {
  return { overdue: [], waiting: rows };
}

// Who holds the Item's claim: the value of its state's claim field; ""
// when the state has no claim or nobody holds it.
export function holderOf(type: Type | undefined, item: Item): string {
  return "";
}

// "Lea has it", "You have it", "Nobody holds it"; "" in a state with no claim.
export function holderLine(type: Type | undefined, item: Item, principal: string, nameOf: NameOf): string {
  return "";
}

// Why the withheld moves are withheld, in words, each once.
export function whyNot(type: Type | undefined, item: Item, principal: string, nameOf: NameOf): string[] {
  return [];
}
