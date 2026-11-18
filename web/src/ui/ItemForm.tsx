// The form for an Item's fields, built from its Type: create (no item) and
// edit (an item, whose state decides what is frozen).
//
// Phase B: signature only.
import type { JsonObject } from "@bufbuild/protobuf";
import type { ReactElement } from "react";
import type { Item, Type } from "../gen/kernel/v1/kernel_pb";

export type ItemFormProps = {
  type: Type;
  // Absent on create.
  item?: Item;
  principals?: string[];
  // The submit button's text: Create, Save.
  submitLabel: string;
  // Create: the filled fields (values.createFields). Edit: the changed
  // fields only, null for an emptied one (values.patchFields). A rejection
  // is shown as the server's refusal; the form keeps what was typed.
  onSubmit: (fields: JsonObject) => Promise<void>;
};

export function ItemForm(_props: ItemFormProps): ReactElement {
  throw new Error("not implemented");
}
