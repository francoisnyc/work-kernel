// The moves an Item's Type declares from its state, as buttons. Read from
// the Type, never evaluated: guards, allowed and required are the server's
// business; `required` is only read to ask for the fields first.
import type { Type } from "@gen/kernel/v1/kernel_pb";
import type { Audience } from "./people";
import { todo } from "./todo";

export type Move = { to: string; label: string; required: string[] };

// The Type's transitions from `state`, in declared order, without those
// only the system makes (allowed names only principal:system:...). A
// customer gets only the moves whose allowed names creator.
export function movesFrom(_type: Type, _state: string, _audience: Audience): Move[] {
  return todo();
}

// Whether the Type declares a move from -> to that a person may ask for:
// what a board column reads to accept a drop.
export function declaresMove(_type: Type, _from: string, _to: string): boolean {
  return todo();
}
