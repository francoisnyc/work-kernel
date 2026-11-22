// "Waiting for you": the vacation requests whose approver is the caller.
// An Item has no time-in-state, so when a request entered submitted comes
// from its history (one read per row), and the order is worked out here.
import type { MessageInitShape } from "@bufbuild/protobuf";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { ItemEventKind, type Item, type ItemEvent, type SearchItemsRequestSchema } from "@gen/kernel/v1/kernel_pb";
import { parseInstant } from "./countdown";

export type InboxRow = {
  item: Item;
  // When it last entered its state (ISO), else its create time.
  waitingSince: string;
  // The reason of an approval refused since then ("insufficient_balance").
  refused?: string;
  // An approval is being checked (an open hold).
  checking: boolean;
};

const me = { kind: { case: "stringValue" as const, value: "me" } };

export function inboxSearch(ns: string): MessageInitShape<typeof SearchItemsRequestSchema> {
  return {
    parent: ns,
    filter: { types: [`${ns}/types/vacation_request`], states: ["submitted"], fields: [{ field: "f_approver", eq: me }] },
    pageSize: 500,
  };
}

export const iso = (t: Timestamp | undefined): string => (t ? timestampDate(t).toISOString() : "");

export function inboxRow(item: Item, events: ItemEvent[]): InboxRow {
  let since = -1;
  events.forEach((ev, i) => {
    if (ev.kind === ItemEventKind.TRANSITIONED && ev.toState === item.state) since = i;
  });
  let refused: string | undefined;
  for (const ev of events.slice(since + 1)) if (ev.kind === ItemEventKind.TRANSITION_DENIED) refused = ev.reason;
  return {
    item,
    waitingSince: since >= 0 ? iso(events[since]!.createTime) : iso(item.createTime),
    refused,
    checking: item.hold !== undefined,
  };
}

// Oldest first; ties by name. A new array.
export function inboxOrder(rows: InboxRow[]): InboxRow[] {
  return [...rows].sort((a, b) => parseInstant(a.waitingSince) - parseInstant(b.waitingSince) || (a.item.name < b.item.name ? -1 : a.item.name > b.item.name ? 1 : 0));
}

// How long since, on the wall clock: "45 min", "3 h", "2 days".
export function waitedLabel(since: string, now: Date): string {
  const s = (now.getTime() - parseInstant(since)) / 1000;
  if (s < 60) return "less than a minute";
  if (s < 3600) return `${Math.floor(s / 60)} min`;
  if (s < 86_400) return `${Math.floor(s / 3600)} h`;
  const d = Math.floor(s / 86_400);
  return `${d} ${d === 1 ? "day" : "days"}`;
}
