// Form values to and from Item.fields. A form holds every field as text
// (or a boolean for a bool); the wire holds plain JSON, an int as a string.
import type { JsonObject, JsonValue } from "@bufbuild/protobuf";
import { FieldType, type Field, type Type } from "../gen/kernel/v1/kernel_pb";

// Field ID -> what the input holds. "" is an empty input.
export type FormValues = Record<string, string | boolean>;

// The form's starting values for an Item's fields ({} or undefined on
// create): every declared field, unset ones as "" (false for a bool).
export function formValues(type: Type, fields: JsonObject | undefined): FormValues {
  const out: FormValues = {};
  for (const f of type.fields) {
    const v = fields?.[f.id];
    if (f.type === FieldType.BOOL) out[f.id] = v === true;
    else out[f.id] = v === undefined || v === null ? "" : String(v);
  }
  return out;
}

// A text input's value as it is sent: a string as typed, anything else
// trimmed.
function sent(field: Field, value: string | boolean | undefined): string {
  const text = typeof value === "string" ? value : "";
  return field.type === FieldType.STRING ? text : text.trim();
}

// CreateItem's fields: only the fields filled in. A bool is sent only when
// ticked.
export function createFields(type: Type, form: FormValues): JsonObject {
  const out: JsonObject = {};
  for (const f of type.fields) {
    if (f.type === FieldType.BOOL) {
      if (form[f.id] === true) out[f.id] = true;
    } else if (sent(f, form[f.id]) !== "") {
      out[f.id] = sent(f, form[f.id]);
    }
  }
  return out;
}

// UpdateItem's patch: only the fields the user changed; an emptied field
// is null (clears it). {} when nothing changed.
export function patchFields(type: Type, original: JsonObject | undefined, form: FormValues): JsonObject {
  const before = formValues(type, original);
  const out: JsonObject = {};
  for (const f of type.fields) {
    if (f.type === FieldType.BOOL) {
      // A checkbox cannot say "unset": ticking sends true, unticking false.
      if (form[f.id] !== before[f.id]) out[f.id] = form[f.id] === true;
      continue;
    }
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

// localInput is a stored instant as a datetime-local input shows it; "" for
// anything that is not an instant.
//
// Phase B stub: not implemented.
export function localInput(_stored: string): string {
  throw new Error("localInput is not implemented");
}

// instantOf is a datetime-local input's text (with or without seconds) as
// it is sent: the instant in UTC, with an offset (Z). "" for an empty or
// unreadable input.
//
// Phase B stub: not implemented.
export function instantOf(_local: string): string {
  throw new Error("instantOf is not implemented");
}
