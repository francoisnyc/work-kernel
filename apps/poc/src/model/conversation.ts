// An incident's conversation: the replies (public) and notes (internal)
// contained in it, oldest first. In the portal, the replies interleaved
// with the status changes the customer's history shows.
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { ItemEventKind, type Item, type ItemEvent } from "@gen/kernel/v1/kernel_pb";
import { typeIdOf } from "./labels";

export type Message = { name: string; kind: "reply" | "note" | "comment"; author: string; at: string; body: string };

const KINDS = new Set(["reply", "note", "comment"]);
const iso = (t: { seconds: bigint; nanos: number } | undefined) => (t ? timestampDate(t as never).toISOString() : "");
const byTime = (a: { at: string; key: string }, b: { at: string; key: string }) =>
  a.at < b.at ? -1 : a.at > b.at ? 1 : a.key < b.key ? -1 : a.key > b.key ? 1 : 0;

// The messages among an Item's children (replies, notes, comments), by
// create time then name; every other child is left out.
export function messagesOf(children: Item[]): Message[] {
  return children
    .filter((c) => KINDS.has(typeIdOf(c.type)))
    .map((c) => ({
      name: c.name,
      kind: typeIdOf(c.type) as Message["kind"],
      author: c.creatorOnBehalfOf !== "" ? c.creatorOnBehalfOf : c.creator,
      at: iso(c.createTime),
      body: typeof c.fields?.f_body === "string" ? c.fields.f_body : "",
    }))
    .sort((a, b) => byTime({ at: a.at, key: a.name }, { at: b.at, key: b.name }));
}

export type PortalEntry = { kind: "message"; message: Message } | { kind: "status"; at: string; state: string };

// The portal's thread: the replies and the moves, by time.
export function portalThread(replies: Item[], events: ItemEvent[]): PortalEntry[] {
  const entries: (PortalEntry & { key: string; at: string })[] = [
    ...messagesOf(replies).map((m) => ({ kind: "message" as const, message: m, at: m.at, key: m.name })),
    ...events
      .filter((e) => e.kind === ItemEventKind.TRANSITIONED)
      .map((e) => ({ kind: "status" as const, at: iso(e.createTime), state: e.toState, key: String(e.itemSeq) })),
  ];
  return entries.sort(byTime).map(({ key: _key, ...e }) => e as PortalEntry);
}
