// Form values to and from Item.fields. A form holds every field as text
// (or a boolean for a bool); the wire holds plain JSON, an int as a string.
//
// Phase B: signatures only.
import type { JsonObject, JsonValue } from "@bufbuild/protobuf";
import type { FieldType, Type } from "../gen/kernel/v1/kernel_pb";

// Field ID -> what the input holds. "" is an empty input.
export type FormValues = Record<string, string | boolean>;

// The form's starting values for an Item's fields ({} or undefined on
// create): every declared field, unset ones as "" (false for a bool).
export function formValues(_type: Type, _fields: JsonObject | undefined): FormValues {
  throw new Error("not implemented");
}

// CreateItem's fields: only the fields filled in. A bool is sent only when
// ticked.
export function createFields(_type: Type, _form: FormValues): JsonObject {
  throw new Error("not implemented");
}

// UpdateItem's patch: only the fields the user changed; an emptied field
// is null (clears it). {} when nothing changed.
export function patchFields(_type: Type, _original: JsonObject | undefined, _form: FormValues): JsonObject {
  throw new Error("not implemented");
}

// A field filter's value from the text typed: a bool field takes true or
// false, everything else the text (an int as a string, "me" for a
// principal).
export function filterValue(_fieldType: FieldType, _text: string): JsonValue {
  throw new Error("not implemented");
}
