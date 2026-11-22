// The moves an Item's Type declares from its state, as buttons. Read from
// the Type, never evaluated: guards, allowed and required are the server's
// business; `required` is only read to ask for the fields first.
import type { Item, Transition, Type } from "@gen/kernel/v1/kernel_pb";
import { transitionLabel } from "./labels";
import type { Audience } from "./people";

export type Move = { to: string; label: string; required: string[] };

// No person makes it: allowed names only principal:system:... (the timer),
// or nobody at all (allowed: [nobody], Jira 2.4b: only a reaction, by
// consent).
export function isSystemOnly(t: Transition): boolean {
  return t.allowed.length > 0 && t.allowed.every((a) => a === "nobody" || a.startsWith("principal:system:"));
}

// The Type's transitions from `state`, in declared order, without those
// no person makes (isSystemOnly: the timer's, or nobody's). A
// customer gets only the moves whose allowed names creator.
export function movesFrom(type: Type, state: string, audience: Audience): Move[] {
  return type.transitions
    .filter((t) => t.fromState === state && !isSystemOnly(t))
    .filter((t) => audience === "staff" || t.allowed.includes("creator"))
    .map((t) => ({ to: t.toState, label: transitionLabel(type.typeId, t.fromState, t.toState, audience), required: [...t.required] }));
}

// Whether the Type declares a move from -> to that a person may ask for:
// what a board column reads to accept a drop.
export function declaresMove(type: Type, from: string, to: string): boolean {
  return type.transitions.some((t) => t.fromState === from && t.toState === to && !isSystemOnly(t));
}

// Who is looking: their principal and the roles the dev-token file gives
// them (there is no "who am I" in the API).
export type Viewer = { principal: string; roles: string[] };

// Whether one entry of `allowed` names the viewer plainly: the requester
// (the user the Item was created for, else its creator), the person a
// principal field holds, a role they hold, or them by name. A system
// principal never matches a person.
function names(entry: string, item: Item, viewer: Viewer): boolean {
  if (entry === "creator") return (item.creatorOnBehalfOf || item.creator) === viewer.principal;
  if (entry.startsWith("field:")) return item.fields?.[entry.slice("field:".length)] === viewer.principal;
  if (entry.startsWith("role:")) return viewer.roles.includes(entry.slice("role:".length));
  if (entry.startsWith("principal:")) return entry.slice("principal:".length) === viewer.principal;
  return false;
}

// The moves a case page offers its viewer, in declared order: those whose
// `allowed` is empty or names the viewer plainly, never one only the
// system makes. Guards and `except` are not judged: the server refuses
// what does not hold, and the page says why.
export function movesFor(type: Type, item: Item, viewer: Viewer): Move[] {
  return type.transitions
    .filter((t) => t.fromState === item.state && !isSystemOnly(t))
    .filter((t) => t.allowed.length === 0 || t.allowed.some((a) => names(a, item, viewer)))
    .map((t) => ({ to: t.toState, label: transitionLabel(type.typeId, t.fromState, t.toState), required: [...t.required] }));
}
