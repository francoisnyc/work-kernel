// One field's input, chosen by the field's declared type: never by Type.
//
// Phase B: signature only.
import type { ReactElement } from "react";
import type { Field } from "../gen/kernel/v1/kernel_pb";
import type { FieldAccess } from "../model/typeinfo";

export type FieldInputProps = {
  field: Field;
  // Text, or a boolean for a bool field.
  value: string | boolean;
  onChange: (value: string | boolean) => void;
  access: FieldAccess;
  // The Item's state, to say where a frozen field is frozen.
  state?: string;
  // Suggestions for a principal field (the dev users).
  principals?: string[];
};

// The input is labelled with the field's ID. A kernel-written or frozen
// field is disabled; why (and a role-written field's roles) is the
// input's accessible description.
export function FieldInput(_props: FieldInputProps): ReactElement {
  throw new Error("not implemented");
}
