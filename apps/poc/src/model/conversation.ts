// An incident's conversation: the replies (public) and notes (internal)
// contained in it, oldest first. In the portal, the replies interleaved
// with the status changes the customer's history shows.
import type { Item, ItemEvent } from "@gen/kernel/v1/kernel_pb";
import { todo } from "./todo";

export type Message = { name: string; kind: "reply" | "note" | "comment"; author: string; at: string; body: string };

// The messages among an Item's children (replies, notes, comments), by
// create time then name; every other child is left out.
export function messagesOf(_children: Item[]): Message[] {
  return todo();
}

export type PortalEntry = { kind: "message"; message: Message } | { kind: "status"; at: string; state: string };

// The portal's thread: the replies and the moves, by time.
export function portalThread(_replies: Item[], _events: ItemEvent[]): PortalEntry[] {
  return todo();
}
