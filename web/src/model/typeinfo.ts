// What the generic UI reads out of Type definitions (GetType, ListTypes).
// Pure functions: no rule is evaluated here, only declarations are read.
import { FieldType, FieldWriter, type Item, type Transition, type Type } from "../gen/kernel/v1/kernel_pb";

// namespaces/{ns}/types/{type}/versions/{n}, or the versionless
// namespaces/{ns}/types/{type} (version 0).
export function parseTypeName(name: string): { ns: string; typeId: string; version: number } {
  const parts = name.split("/");
  return { ns: parts[1] ?? "", typeId: parts[3] ?? "", version: Number(parts[5] ?? 0) };
}

// The ID at the end of an Item name.
export function itemId(itemName: string): string {
  return itemName.slice(itemName.lastIndexOf("/") + 1);
}

// The first 8 characters of an Item name's ID, for display.
export function shortId(itemName: string): string {
  return itemId(itemName).slice(0, 8);
}

// Byte order, for every sorted list here: the same on every machine.
const byText = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0);

// One Type per type ID, its highest version, ordered by type ID.
export function latestVersions(types: Type[]): Type[] {
  const latest = new Map<string, Type>();
  for (const t of types) {
    const seen = latest.get(t.typeId);
    if (!seen || t.version > seen.version) latest.set(t.typeId, t);
  }
  return [...latest.values()].sort((a, b) => byText(a.typeId, b.typeId));
}

// Whether a form lets the user write a field of an Item in a state.
//   kernel:   only the kernel writes it; never editable.
//   frozen:   the state freezes it; read-only there.
//   editable: anyone who may write the Item; `roles` names the roles the
//             field is reserved to, if any (shown as a hint: the server,
//             not the UI, refuses a user without the role).
//   reserved: the field is reserved to `roles` and the acting user is
//             known to hold none of them; read-only for them.
export type FieldAccess = { mode: "editable" | "kernel" | "frozen" | "reserved"; roles: string[] };

// userRoles: the roles the acting user holds; left out when not known.
export function fieldAccess(type: Type, fieldId: string, state: string, userRoles?: readonly string[]): FieldAccess {
  const field = type.fields.find((f) => f.id === fieldId);
  if (field?.writableBy === FieldWriter.KERNEL) return { mode: "kernel", roles: [] };
  const frozen = type.states.find((s) => s.id === state)?.frozen.includes(fieldId);
  if (frozen) return { mode: "frozen", roles: [] };
  const roles = [...(field?.writableByRoles ?? [])];
  return { mode: mayWrite(field, userRoles) ? "editable" : "reserved", roles };
}

// Whether the acting user may write a field, as far as the Type says:
// never a field only the kernel writes; a field reserved to roles only
// when they hold one (or when their roles are not known: the server
// decides). The state's frozen fields are another question (fieldAccess).
export function mayWrite(
  field: { writableBy: FieldWriter; writableByRoles: string[] } | undefined,
  userRoles?: readonly string[],
): boolean {
  if (!field) return true;
  if (field.writableBy === FieldWriter.KERNEL) return false;
  if (field.writableByRoles.length === 0 || userRoles === undefined) return true;
  return field.writableByRoles.some((r) => userRoles.includes(r));
}

// A transition leaving a state. systemOnly: its `allowed` names only
// principal:system:... entries, so no person can make it (a label read
// from the Type, not a check of who is calling).
export type TransitionView = { transition: Transition; systemOnly: boolean };

// A move no person makes: only the timer (principal:system:*), or nobody
// at all (allowed: [nobody]: only a reaction, by consent).
export function isSystemOnly(t: Transition): boolean {
  return t.allowed.length > 0 && t.allowed.every((a) => a === "nobody" || a.startsWith("principal:system:"));
}

export function transitionsFrom(type: Type, state: string): TransitionView[] {
  return type.transitions
    .filter((t) => t.fromState === state)
    .map((t) => ({ transition: t, systemOnly: isSystemOnly(t) }));
}

// A link an Item of a Type can receive from other Types' Items: found by
// scanning every Type's link types for this Type (or "*") among their
// targets. name is the inverse (how this Item sees it), declared the name
// the source's Type declares, from the source type IDs.
export type IncomingLinkType = { name: string; declared: string; from: string[]; writable: boolean };

export function incomingLinkTypes(types: Type[], typeId: string): IncomingLinkType[] {
  const found = new Map<string, IncomingLinkType>();
  for (const type of types) {
    for (const lt of type.linkTypes) {
      const targetsIt = lt.targets.some((t) => t === "*" || parseTypeName(t).typeId === typeId);
      if (!targetsIt || lt.inverse === "") continue;
      const key = `${lt.inverse}|${lt.name}`;
      const entry = found.get(key) ?? { name: lt.inverse, declared: lt.name, from: [], writable: true };
      if (!entry.from.includes(type.typeId)) entry.from.push(type.typeId);
      if (lt.writableBy === FieldWriter.KERNEL) entry.writable = false;
      found.set(key, entry);
    }
  }
  return [...found.values()]
    .map((l) => ({ ...l, from: [...l.from].sort(byText) }))
    .sort((a, b) => byText(a.name, b.name) || byText(a.declared, b.declared));
}

// The first string field, in the Type's order, that has a value; else the
// type ID and the short ID.
export function displayTitle(type: Type, item: Item): string {
  for (const f of type.fields) {
    const v = item.fields?.[f.id];
    if (f.type === FieldType.STRING && typeof v === "string" && v !== "") return v;
  }
  return `${type.typeId} ${shortId(item.name)}`;
}

// A Type by its label (the Inbox, the look): its own, else the newest
// version's of the same Type ID that has one, else its ID. typeNameOf is
// the Item's type name, for a Type the page does not have. STUB for the
// Phase B tests: the ID only.
export function typeLabel(type: Type | undefined, _types: readonly Type[], typeNameOf = ""): string {
  return type?.typeId ?? (typeNameOf === "" ? "" : parseTypeName(typeNameOf).typeId);
}
