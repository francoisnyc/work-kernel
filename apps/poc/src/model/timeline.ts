// An Item's history as sentences ("sam moved this from To do to In
// progress"). The kernel's own records (reactions, hooks, a reaction's
// writes) are marked system, and a screen shows them only on demand.
import type { ItemEvent, ItemRef } from "@gen/kernel/v1/kernel_pb";
import type { Audience } from "./people";
import { todo } from "./todo";

export type TimelineContext = {
  typeId: string;
  audience: Audience;
  // How to name another Item the event points at (a link's target).
  titleOf: (ref: ItemRef) => string;
};

export type TimelineEntry = { at: string; actor: string; text: string; system: boolean };

// One event as a sentence, or null for an event a screen never shows.
export function describeEvent(_ev: ItemEvent, _ctx: TimelineContext): TimelineEntry | null {
  return todo();
}
