// The Inbox page's model (track 1 item 5, step 4, PR 2): what the page
// asks the kernel and how it words the answer. Pure functions.
//
// The kernel decides who may act: the rows come from `waiting_on`, the
// counts from CountItems, the buttons from each Item's `moves`. Nothing
// here reads `allowed`, `except` or a claim to decide what a person may
// do. The one thing read from the Types is which role names appear in
// some `allowed`, to choose which of the caller's roles get a tab.
//
// STUB: declared so the tests compile; not built yet.
import type { MessageInitShape } from "@bufbuild/protobuf";
import type { Timestamp } from "@bufbuild/protobuf/wkt";
import type {
  CountItemsRequestSchema,
  Item,
  SearchItemsRequestSchema,
  Transition,
  Type,
} from "../gen/kernel/v1/kernel_pb";

// One tab: "Waiting on me", or a role's queue.
export type InboxTab = { key: string; label: string; waitingOn: string; role?: string };

export type InboxOrder = "oldest" | "newest";

// A page of the Inbox.
export const INBOX_PAGE = 50;

// What the page is showing: a tab, an order, an optional place (an Item's
// name; "" for anywhere).
export type InboxView = { parent: string; tab: InboxTab; order: InboxOrder; place: string };

export function inboxTabs(_roles: readonly string[], _types: Type[]): InboxTab[] {
  return [];
}

export function inboxSearch(_v: InboxView, _page: { pageToken?: string; minSeq?: bigint } = {}): MessageInitShape<typeof SearchItemsRequestSchema> {
  return {};
}

export function inboxCount(_v: Omit<InboxView, "order">): MessageInitShape<typeof CountItemsRequestSchema> {
  return {};
}

export function countLabel(_c: { count: number; capped: boolean } | undefined): string {
  return "";
}

export function rowMoves(_type: Type | undefined, _item: Item): Transition[] {
  return [];
}

export function whyYours(_tab: InboxTab, _type: Type | undefined, _item: Item, _principal: string): string {
  return "";
}

export function askedBy(_item: Item): string {
  return "";
}

export function waitingFor(_stateTime: Timestamp | undefined, _now: Date): string {
  return "";
}
