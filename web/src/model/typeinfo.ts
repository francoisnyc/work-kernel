// What the generic UI reads out of Type definitions (GetType, ListTypes).
// Pure functions: no rule is evaluated here, only declarations are read.
//
// Phase B: signatures only.
import type { Item, Transition, Type } from "../gen/kernel/v1/kernel_pb";

// namespaces/{ns}/types/{type}/versions/{n}, or the versionless
// namespaces/{ns}/types/{type} (version 0).
export function parseTypeName(_name: string): { ns: string; typeId: string; version: number } {
  throw new Error("not implemented");
}

// The first 8 characters of an Item name's ID, for display.
export function shortId(_itemName: string): string {
  throw new Error("not implemented");
}

// One Type per type ID, its highest version, ordered by type ID.
export function latestVersions(_types: Type[]): Type[] {
  throw new Error("not implemented");
}

// Whether a form lets the user write a field of an Item in a state.
//   kernel:   only the kernel writes it; never editable.
//   frozen:   the state freezes it; read-only there.
//   editable: anyone who may write the Item; `roles` names the roles the
//             field is reserved to, if any (shown as a hint: the server,
//             not the UI, refuses a user without the role).
export type FieldAccess = { mode: "editable" | "kernel" | "frozen"; roles: string[] };

export function fieldAccess(_type: Type, _fieldId: string, _state: string): FieldAccess {
  throw new Error("not implemented");
}

// A transition leaving a state. systemOnly: its `allowed` names only
// principal:system:... entries, so no person can make it (a label read
// from the Type, not a check of who is calling).
export type TransitionView = { transition: Transition; systemOnly: boolean };

export function transitionsFrom(_type: Type, _state: string): TransitionView[] {
  throw new Error("not implemented");
}

// One inbox search: Items of a Type (any version) in a state, where a
// principal field holds the caller. Built from every transition whose
// `allowed` names field:X.
export type InboxQuery = { type: string; state: string; field: string };

export function inboxQueries(_types: Type[]): InboxQuery[] {
  throw new Error("not implemented");
}

// The transitions an inbox row offers: those leaving the Item's state, in
// its own Type version, whose `allowed` names a field.
export function inboxTransitions(_type: Type, _item: Item): Transition[] {
  throw new Error("not implemented");
}

// A link an Item of a Type can receive from other Types' Items: found by
// scanning every Type's link types for this Type (or "*") among their
// targets. name is the inverse (how this Item sees it), declared the name
// the source's Type declares, from the source type IDs.
export type IncomingLinkType = { name: string; declared: string; from: string[]; writable: boolean };

export function incomingLinkTypes(_types: Type[], _typeId: string): IncomingLinkType[] {
  throw new Error("not implemented");
}

// The first string field, in the Type's order, that has a value; else the
// type ID and the short ID.
export function displayTitle(_type: Type, _item: Item): string {
  throw new Error("not implemented");
}
