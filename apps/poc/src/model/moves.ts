// The moves an Item's Type declares from its state, as buttons. Read from
// the Type, never evaluated: guards, allowed and required are the server's
// business; `required` is only read to ask for the fields first.
import type { Transition, Type } from "@gen/kernel/v1/kernel_pb";
import { transitionLabel } from "./labels";
import type { Audience } from "./people";

export type Move = { to: string; label: string; required: string[] };

// Only the system makes it: allowed names principal:system:... and nobody else.
const systemOnly = (t: Transition) => t.allowed.length > 0 && t.allowed.every((a) => a.startsWith("principal:system:"));

// The Type's transitions from `state`, in declared order, without those
// only the system makes (allowed names only principal:system:...). A
// customer gets only the moves whose allowed names creator.
export function movesFrom(type: Type, state: string, audience: Audience): Move[] {
  return type.transitions
    .filter((t) => t.fromState === state && !systemOnly(t))
    .filter((t) => audience === "staff" || t.allowed.includes("creator"))
    .map((t) => ({ to: t.toState, label: transitionLabel(type.typeId, t.fromState, t.toState, audience), required: [...t.required] }));
}

// Whether the Type declares a move from -> to that a person may ask for:
// what a board column reads to accept a drop.
export function declaresMove(type: Type, from: string, to: string): boolean {
  return type.transitions.some((t) => t.fromState === from && t.toState === to && !systemOnly(t));
}
