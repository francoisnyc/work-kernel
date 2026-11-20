// Form values to and from Item.fields. A form holds every field as text
// (or a boolean for a bool); the wire holds plain JSON, an int as a string.
import type { JsonObject, JsonValue } from "@bufbuild/protobuf";
import { FieldType, type Field, type Type } from "../gen/kernel/v1/kernel_pb";

// What one input holds: text ("" is an empty input), a boolean for a bool,
// or the members of a multi-value field (a set, kept sorted; [] is unset).
export type FormValue = string | boolean | string[];

// Field ID -> what the input holds.
export type FormValues = Record<string, FormValue>;

// The form's starting values for an Item's fields ({} or undefined on
// create): every declared field, unset ones as "" (false for a bool).
export function formValues(type: Type, fields: JsonObject | undefined): FormValues {
  const out: FormValues = {};
  for (const f of type.fields) {
    const v = fields?.[f.id];
    if (f.multiple) out[f.id] = membersOf(v);
    else if (f.type === FieldType.BOOL) out[f.id] = v === true;
    else if (f.type === FieldType.DATETIME) out[f.id] = typeof v === "string" ? localInput(v) : "";
    else out[f.id] = v === undefined || v === null ? "" : String(v);
  }
  return out;
}

// The members of a stored set, sorted as the server keeps them; [] for an
// unset field.
function membersOf(v: JsonValue | FormValue | undefined): string[] {
  return Array.isArray(v) ? v.map(String).sort() : [];
}

// A text input's value as it is sent: a string as typed, anything else
// trimmed.
function sent(field: Field, value: FormValue | undefined): string {
  const text = typeof value === "string" ? value : "";
  // A datetime is edited on the browser's clock and sent in UTC.
  if (field.type === FieldType.DATETIME) return instantOf(text.trim());
  return field.type === FieldType.STRING ? text : text.trim();
}

// CreateItem's fields: only the fields filled in. A bool is sent only when
// ticked.
export function createFields(type: Type, form: FormValues): JsonObject {
  const out: JsonObject = {};
  for (const f of type.fields) {
    if (f.multiple) {
      const members = membersOf(form[f.id]);
      if (members.length > 0) out[f.id] = members;
    } else if (f.type === FieldType.BOOL) {
      if (form[f.id] === true) out[f.id] = true;
    } else if (sent(f, form[f.id]) !== "") {
      out[f.id] = sent(f, form[f.id]);
    }
  }
  return out;
}

// UpdateItem's patch: only the fields the user changed; an emptied field
// is null (clears it). A changed set is sent as a set operation, the
// members added and removed, never as a new list: someone else's members,
// added meanwhile, stay. {} when nothing changed.
export function patchFields(type: Type, original: JsonObject | undefined, form: FormValues): JsonObject {
  const before = formValues(type, original);
  const out: JsonObject = {};
  for (const f of type.fields) {
    if (f.multiple) {
      const was = membersOf(before[f.id]);
      const now = membersOf(form[f.id]);
      const op: JsonObject = {};
      const add = now.filter((m) => !was.includes(m));
      const remove = was.filter((m) => !now.includes(m));
      if (add.length > 0) op.add = add;
      if (remove.length > 0) op.remove = remove;
      if (add.length + remove.length > 0) out[f.id] = op;
      continue;
    }
    if (f.type === FieldType.BOOL) {
      // A checkbox cannot say "unset": ticking sends true, unticking false.
      if (form[f.id] !== before[f.id]) out[f.id] = form[f.id] === true;
      continue;
    }
    // Changed or not is judged on what the input holds: an instant nobody
    // touched is not sent, so its microseconds are never cut to a second.
    if (f.type === FieldType.DATETIME && form[f.id] === before[f.id]) continue;
    const now = sent(f, form[f.id]);
    if (now !== before[f.id]) out[f.id] = now === "" ? null : now;
  }
  return out;
}

// A field filter's value from the text typed: a bool field takes true or
// false, everything else the text (an int as a string, "me" for a
// principal).
export function filterValue(fieldType: FieldType, text: string): JsonValue {
  return fieldType === FieldType.BOOL ? text === "true" : text;
}

// --- date-time fields ---
//
// The wire holds an instant as an RFC 3339 string in UTC
// (2026-12-04T17:00:00.000000Z). A form holds it as a datetime-local input
// does: the same instant on the browser's clock, without an offset, to the
// second (2026-12-04T12:00:00 in New York).

const two = (n: number) => String(n).padStart(2, "0");

// localInput is a stored instant as a datetime-local input shows it; "" for
// anything that is not an instant. A fraction of a second is not shown.
export function localInput(stored: string): string {
  const at = new Date(stored);
  if (stored === "" || Number.isNaN(at.getTime())) return "";
  const date = `${String(at.getFullYear()).padStart(4, "0")}-${two(at.getMonth() + 1)}-${two(at.getDate())}`;
  return `${date}T${two(at.getHours())}:${two(at.getMinutes())}:${two(at.getSeconds())}`;
}

// instantOf is a datetime-local input's text (with or without seconds) as
// it is sent: the instant in UTC, with an offset (Z). "" for an empty or
// unreadable input. A text without an offset is read on the browser's
// clock, which is what the input shows.
export function instantOf(local: string): string {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?$/.test(local)) return "";
  const at = new Date(local);
  return Number.isNaN(at.getTime()) ? "" : at.toISOString();
}
