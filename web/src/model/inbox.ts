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
  WithheldReason,
  type CountItemsRequestSchema,
  type Item,
  type ItemEvent,
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
  // its queue (Inbox PR 2b).
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
  return [ME, ...held.map((r) => ({ key: `role:${r}`, label: roleLabel(r), waitingOn: `role:${r}`, role: r }))];
}

function filterOf(v: Pick<InboxView, "tab" | "place" | "claimed">) {
  return {
    waitingOn: v.tab.waitingOn,
    ...(v.place === "" ? {} : { in: v.place }),
    ...(v.claimed ? { claimed: true } : {}),
  };
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
//   - on a role's tab, nothing: the tab's name says it (PR 2b);
//   - on Waiting on me, "sent back to you" when the Item's state says it
//     waits on its requester (GetType's waits_on_requester) and the caller
//     is its requester (askedBy);
//   - on Waiting on me, the principal fields that hold the caller, by
//     label (the ID without one): "Approver: you" (the Inbox, the look);
//     nothing when no
//     field does (the kernel put the
//     Item in the view; the browser does not re-derive why).
export function whyYours(tab: InboxTab, type: Type | undefined, item: Item, principal: string): string {
  if (tab.role !== undefined) return "";
  const state = type?.states.find((s) => s.id === item.state);
  if (state?.waitsOnRequester && askedBy(item) === principal) return "sent back to you";
  const labels = (type?.fields ?? [])
    .filter((f) => f.type === FieldType.PRINCIPAL && item.fields?.[f.id] === principal)
    .map((f) => f.label || f.id);
  return labels.length === 0 ? "" : `${labels.join(", ")}: you`;
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
// Words for what the kernel sends: the Type's hints (labels, primary,
// summary, due), State.claim, Item.moves and Item.withheld. Nothing here
// decides who may act.

// A person's name for the page (people.personName), or the ID.
export type NameOf = (principal: string) => string;

// A role's ID made readable for its tab: "desk_agent" is "Desk agent".
// The kernel has no role labels (Later).
export function roleLabel(role: string): string {
  const words = role.replaceAll("_", " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

// A state's label, or its ID when the Type gives none.
export function stateLabel(type: Type | undefined, state: string): string {
  return type?.states.find((s) => s.id === state)?.label || state;
}

// The row's main button and the rest (the "More moves" disclosure): the
// move marked primary when the server lists it, else the first it lists.
// Together they are exactly Item.moves, in the server's order.
export function splitMoves(type: Type | undefined, item: Item): { main?: Transition; others: Transition[] } {
  const moves = rowMoves(type, item);
  const main = moves.find((t) => t.primary) ?? moves[0];
  return { main, others: moves.filter((t) => t !== main) };
}

// The Type's summary fields that hold a value, as "Label: value", in the
// summary's order; a principal is named.
export function summaryLine(type: Type | undefined, item: Item, nameOf: NameOf): string[] {
  const out: string[] = [];
  for (const id of type?.summary ?? []) {
    const field = type?.fields.find((f) => f.id === id);
    const value = item.fields?.[id];
    if (value === undefined || value === null || value === "" || (Array.isArray(value) && value.length === 0)) continue;
    const show = (v: unknown) =>
      field?.type === FieldType.PRINCIPAL && typeof v === "string" ? nameOf(v) : typeof v === "boolean" ? (v ? "yes" : "no") : String(v);
    out.push(`${field?.label || id}: ${Array.isArray(value) ? value.map(show).join(", ") : show(value)}`);
  }
  return out;
}

// "Ana asks", or "You asked for this one".
export function askedLine(item: Item, principal: string, nameOf: NameOf): string {
  const who = askedBy(item);
  return who === principal ? "You asked for this one" : `${nameOf(who)} asks`;
}

// The largest unit that fits, rounded down: "3 h". undefined under a second.
function span(ms: number): string | undefined {
  const seconds = Math.floor(ms / 1000);
  if (seconds < 1) return undefined;
  const [size, unit] = units.find(([s]) => seconds >= s)!;
  return `${Math.floor(seconds / size)} ${unit}`;
}

// "waiting 3 h"; "in hand 3 h" in a state with a claim (since the claim),
// "in hand just now" under a second.
export function waitedLine(type: Type | undefined, item: Item, now: Date): string {
  const claimed = type?.states.find((s) => s.id === item.state)?.claim;
  if (!claimed) return waitingFor(item.stateTime, now);
  if (!item.stateTime) return "";
  return `in hand ${span(now.getTime() - timestampDate(item.stateTime).getTime()) ?? "just now"}`;
}

// When the Item is due in its state (the state's `due` field), and
// whether that has passed. Clock time.
export type Due = { text: string; overdue: boolean };
export function dueOf(type: Type | undefined, item: Item, now: Date): Due | undefined {
  const field = type?.states.find((s) => s.id === item.state)?.due;
  const value = field ? item.fields?.[field] : undefined;
  if (typeof value !== "string") return undefined;
  const at = Date.parse(value);
  if (Number.isNaN(at)) return undefined;
  const ahead = at - now.getTime();
  if (ahead > 0) {
    const s = span(ahead);
    if (s) return { text: `due in ${s}`, overdue: false };
  }
  const s = span(-ahead);
  return { text: s ? `due ${s} ago` : "due now", overdue: true };
}

// The instant the Item is due by in its state (the state's `due` field),
// as the server sends it; "" when none.
export function dueInstant(type: Type | undefined, item: Item): string {
  const field = type?.states.find((s) => s.id === item.state)?.due;
  const value = field ? item.fields?.[field] : undefined;
  return typeof value === "string" ? value : "";
}

// Overdue first, then the rest, each in the order given. Worked out on
// the rows loaded: an overdue row on a later page shows once it is loaded.
export function groupRows(rows: Item[], typeOf: (name: string) => Type | undefined, now: Date): { overdue: Item[]; waiting: Item[] } {
  const overdue: Item[] = [];
  const waiting: Item[] = [];
  for (const it of rows) (dueOf(typeOf(it.type), it, now)?.overdue ? overdue : waiting).push(it);
  return { overdue, waiting };
}

// Who holds the Item's claim: the value of its state's claim field; ""
// when the state has no claim or nobody holds it.
export function holderOf(type: Type | undefined, item: Item): string {
  const claim = type?.states.find((s) => s.id === item.state)?.claim;
  const held = claim ? item.fields?.[claim] : undefined;
  return typeof held === "string" ? held : "";
}

// "Lea has it", "You have it", "Nobody holds it"; "" in a state with no claim.
export function holderLine(type: Type | undefined, item: Item, principal: string, nameOf: NameOf): string {
  if (!type?.states.find((s) => s.id === item.state)?.claim) return "";
  const holder = holderOf(type, item);
  if (holder === "") return "Nobody holds it";
  return holder === principal ? "You have it" : `${nameOf(holder)} has it`;
}

// Why the withheld moves are withheld (Item.withheld), in words, each once.
export function whyNot(type: Type | undefined, item: Item, principal: string, nameOf: NameOf): string[] {
  const out: string[] = [];
  for (const w of item.withheld) {
    let text = "";
    if (w.reason === WithheldReason.OWN_REQUEST) text = "You can't handle your own request";
    else if (w.reason === WithheldReason.OWN_CONTAINER) text = "You can't decide on your own request";
    else if (w.reason === WithheldReason.CLAIMED_BY_OTHER) {
      const holder = holderOf(type, item);
      text = holder === "" ? "Nobody holds it" : `${nameOf(holder)} has it`;
    }
    if (text !== "" && !out.includes(text)) out.push(text);
  }
  return out;
}

// --- The Inbox for people, PR 2c: the side panel, several rows, keys. ---

// The moves the selection bar offers for the selected rows: a move every
// row lists in Item.moves, when all rows are of one Type (version), and
// only one with no required fields; in the first row's order.
export function bulkMoves(rows: Item[], typeOf: (name: string) => Type | undefined): Transition[] {
  const first = rows[0];
  if (!first || rows.some((r) => r.type !== first.type)) return [];
  const type = typeOf(first.type);
  // Each row's own move to that state (rows may be in different states)
  // must be listed for it and ask for no field.
  const plain = (r: Item, to: string) => r.moves.includes(to) && rowMoves(type, r).find((t) => t.toState === to)?.required.length === 0;
  return rowMoves(type, first).filter((t) => rows.every((r) => plain(r, t.toState)));
}

// The row the panel moves to after a move: the first row after `moved` in
// the list before the move that is still listed after it; else the last
// still listed before it; else none.
export function nextAfter(before: string[], after: string[], moved: string): string | undefined {
  const at = before.indexOf(moved);
  const still = new Set(after);
  for (let i = at + 1; i < before.length; i++) if (still.has(before[i]!)) return before[i];
  for (let i = at - 1; i >= 0; i--) if (still.has(before[i]!)) return before[i];
  return undefined;
}

// What a key does on the list: J next row, K previous, Enter open the
// panel, X select, P the main move. Nothing while typing (a text field,
// a select, an editable element) or with Ctrl, Meta or Alt held.
export type InboxKey = "next" | "prev" | "open" | "select" | "main";
const NOT_TYPING = new Set(["checkbox", "radio", "button", "submit", "reset"]);
export function inboxKey(e: { key: string; ctrlKey?: boolean; metaKey?: boolean; altKey?: boolean; target: EventTarget | null }): InboxKey | undefined {
  if (e.ctrlKey || e.metaKey || e.altKey) return undefined;
  const t = e.target;
  if (t instanceof HTMLElement) {
    if (t instanceof HTMLTextAreaElement || t instanceof HTMLSelectElement) return undefined;
    if (t instanceof HTMLInputElement && !NOT_TYPING.has(t.type)) return undefined;
    const editable = t.getAttribute("contenteditable");
    if (t.isContentEditable || (editable !== null && editable !== "false")) return undefined;
  }
  switch (e.key) {
    case "j":
    case "J":
      return "next";
    case "k":
    case "K":
      return "prev";
    case "Enter":
      return "open";
    case "x":
    case "X":
      return "select";
    case "p":
    case "P":
      return "main";
    default:
      return undefined;
  }
}

// The Inbox's URL with a row's panel open ("#/inbox?item=<id>"), or none.
export function panelHash(id: string): string {
  return id === "" ? "#/inbox" : `#/inbox?item=${id}`;
}

// The open panel's row ID from the Inbox's URL; "" when none.
export function panelOf(hash: string): string {
  const [path = "", query = ""] = hash.replace(/^#\/?/, "").split("?");
  return path === "inbox" ? (new URLSearchParams(query).get("item") ?? "") : "";
}

// --- The Inbox for people, PR 3b: an agent's request, shown as one. ---

const AGENT = "agent:";

// Why a row is an agent's request: the agent (its ID without "agent:")
// and whom it acted for ("Ana", "you", or "" for nobody). From Item.
// state_actor when an agent put the Item in its state, else from its
// creator when an agent created it; undefined when neither.
export type AgentMark = { agent: string; forWhom: string };
export function agentMark(item: Item, principal: string, nameOf: NameOf): AgentMark | undefined {
  const [actor, behalf] = item.stateActor.startsWith(AGENT)
    ? [item.stateActor, item.stateActorOnBehalfOf]
    : item.creator.startsWith(AGENT)
      ? [item.creator, item.creatorOnBehalfOf]
      : ["", ""];
  if (actor === "") return undefined;
  const forWhom = behalf === "" ? "" : behalf === principal ? "you" : nameOf(behalf);
  // Its display name from the dev-token file (the Inbox, the look), else
  // its ID without "agent:".
  const named = nameOf(actor);
  return { agent: named !== actor ? named : actor.slice(AGENT.length), forWhom };
}

// The row's badge: "Agent request · for Ana", or "Agent request".
export function agentBadge(mark: AgentMark): string {
  return mark.forWhom === "" ? "Agent request" : `Agent request · for ${mark.forWhom}`;
}

// The panel's line: "triage-bot, for Ana", or "triage-bot".
export function agentLine(mark: AgentMark): string {
  return mark.forWhom === "" ? mark.agent : `${mark.agent}, for ${mark.forWhom}`;
}

// What the agent wrote: the Type's fields whose latest setting event (in
// the Item's history) was an agent's, with the Item's value now, in the
// Type's order. A field a person set after the agent is the person's; one
// the agent cleared is left out. The value is text as written: the page
// shows it as a plain text node.
export type AgentWritten = { field: string; label: string; value: string };
export function agentWritten(type: Type | undefined, item: Item, events: ItemEvent[], nameOf: NameOf): AgentWritten[] {
  const lastWriter = new Map<string, string>();
  for (const e of [...events].sort((a, b) => (a.itemSeq < b.itemSeq ? -1 : a.itemSeq > b.itemSeq ? 1 : 0))) {
    for (const f of Object.keys(e.fields ?? {})) lastWriter.set(f, e.actor);
  }
  const out: AgentWritten[] = [];
  for (const f of type?.fields ?? []) {
    if (!lastWriter.get(f.id)?.startsWith(AGENT)) continue;
    const v = item.fields?.[f.id];
    if (v === undefined || v === null || v === "" || (Array.isArray(v) && v.length === 0)) continue;
    const one = (x: unknown) => (f.type === FieldType.PRINCIPAL && typeof x === "string" ? nameOf(x) : String(x));
    out.push({ field: f.id, label: f.label || f.id, value: Array.isArray(v) ? v.map(one).join(", ") : one(v) });
  }
  return out;
}
