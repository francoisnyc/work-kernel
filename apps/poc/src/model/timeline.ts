// An Item's history as sentences ("sam moved this from To do to In
// progress"). The kernel's own records (reactions, hooks, a reaction's
// writes) are marked system, and a screen shows them only on demand.
import type { JsonValue } from "@bufbuild/protobuf";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { ItemEventKind, type ItemEvent, type ItemRef } from "@gen/kernel/v1/kernel_pb";
import { shortInstant, parseInstant } from "./countdown";
import { fieldLabel, optionLabel, stateLabel } from "./labels";
import { personName, type Audience } from "./people";

export type TimelineContext = {
  typeId: string;
  audience: Audience;
  // How to name another Item the event points at (a link's target).
  titleOf: (ref: ItemRef) => string;
};

export type TimelineEntry = { at: string; actor: string; text: string; system: boolean };

// Fields whose value is long text: said as "changed", never repeated.
const LONG = new Set(["f_description", "f_body", "f_goal"]);
// Fields the history never names: an ordering key.
const QUIET: Record<string, string> = { f_rank: "changed the rank" };
// Fields the kernel writes alongside a person's move (resolved at): the
// move says the person's fields only.
const KERNEL_ALONG = new Set(["f_resolved_at"]);

// The apps' enum fields: their values are named (wontfix -> Won't fix);
// anything else is shown as written.
const ENUMS = new Set(["f_priority", "f_resolution", "f_impact", "f_outcome", "f_metric"]);

const isSystem = (actor: string) => !/^(user|customer|agent):/.test(actor);

function valueText(field: string, v: JsonValue): string {
  if (typeof v === "string") {
    if (/^(user|customer|agent):/.test(v)) return personName(v);
    if (!Number.isNaN(parseInstant(v))) return shortInstant(v);
    return ENUMS.has(field) ? optionLabel(field, v) : v;
  }
  return String(v);
}

function fieldChanges(ev: ItemEvent, skipKernel: boolean): string[] {
  const out: string[] = [];
  const ops = (ev.setOps ?? {}) as Record<string, { add?: JsonValue[]; remove?: JsonValue[] }>;
  for (const [field, v] of Object.entries(ev.fields ?? {})) {
    const label = fieldLabel(field).toLowerCase();
    if (skipKernel && KERNEL_ALONG.has(field)) continue;
    if (QUIET[field]) out.push(QUIET[field]!);
    else if (ops[field]) {
      for (const a of ops[field]!.add ?? []) out.push(`added ${valueText(field, a)} to ${label}`);
      for (const r of ops[field]!.remove ?? []) out.push(`removed ${valueText(field, r)} from ${label}`);
    } else if (v === null) out.push(`cleared ${label}`);
    else if (LONG.has(field)) out.push(`changed ${label}`);
    else if (Array.isArray(v)) out.push(`set ${label} to ${v.map((x) => valueText(field, x)).join(", ")}`);
    else out.push(`set ${label} to ${valueText(field, v)}`);
  }
  return out;
}

const linkWords: Record<string, (t: string) => string> = {
  blocks: (t) => `blocks ${t}`,
  relates: (t) => `relates to ${t}`,
  duplicates: (t) => `duplicates ${t}`,
  caused_by: (t) => `caused by ${t}`,
};
const membership = new Set(["epic", "sprint"]);

function linkText(ev: ItemEvent, ctx: TimelineContext, added: boolean): string {
  const target = ev.linkTarget ? ctx.titleOf(ev.linkTarget) : "an item";
  const type = ev.linkType;
  if (membership.has(type)) return added ? `added this to ${type} ${target}` : `removed this from ${type} ${target}`;
  const words = (linkWords[type] ?? ((t: string) => `${type.replace(/_/g, " ")} ${t}`))(target);
  return added ? `linked this: ${words}` : `removed link: ${words}`;
}

const shortReaction = (r: string) => r.slice(r.lastIndexOf(":") + 1);

// One event as a sentence, or null for an event a screen never shows.
export function describeEvent(ev: ItemEvent, ctx: TimelineContext): TimelineEntry | null {
  const at = ev.createTime ? timestampDate(ev.createTime).toISOString() : "";
  const system = isSystem(ev.actor);
  const actor = system ? "System" : personName(ev.actor);
  const say = (text: string, sys = system): TimelineEntry => ({ at, actor, text, system: sys });
  const state = (s: string) => stateLabel(ctx.typeId, s, ctx.audience);
  switch (ev.kind) {
    case ItemEventKind.CREATED:
      return say("created this");
    case ItemEventKind.TRANSITIONED: {
      const parts = [`moved this from ${state(ev.fromState)} to ${state(ev.toState)}`, ...fieldChanges(ev, true)];
      return say(parts.join(", "));
    }
    case ItemEventKind.UPDATED: {
      if (ev.readers) return say("changed who can see this");
      if (ev.inherit) return say("let everyone who sees its container see this");
      if (ev.moved) return say("moved this to another place");
      const parts = fieldChanges(ev, false);
      return parts.length === 0 ? null : say(parts.join(", "));
    }
    case ItemEventKind.LINK_ADDED:
      return say(linkText(ev, ctx, true));
    case ItemEventKind.LINK_REMOVED:
      return say(linkText(ev, ctx, false));
    case ItemEventKind.TRANSITION_REQUESTED:
      return say(`asked to move this to ${state(ev.requestedState)}`);
    case ItemEventKind.TRANSITION_DENIED:
      return say(`was refused the move to ${state(ev.requestedState)}`, true);
    case ItemEventKind.TRANSITION_FAILED:
      return say(`could not check the move to ${state(ev.requestedState)}`, true);
    case ItemEventKind.REACTION_APPLIED:
    case ItemEventKind.REACTION_FAILED:
    case ItemEventKind.REACTION_SKIPPED:
      return say(`ran an automation (${shortReaction(ev.reaction)})`, true);
    case ItemEventKind.HOOK_RAN:
      return say("sent a notification", true);
    case ItemEventKind.HOOK_FAILED:
      return say("could not send a notification", true);
    default:
      return null;
  }
}
