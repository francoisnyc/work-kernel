// The form for an Item's fields, built from its Type: create (no item) and
// edit (an item, whose state decides what is frozen).
import type { JsonObject } from "@bufbuild/protobuf";
import { useState, type FormEvent, type ReactElement } from "react";
import type { Item, Type } from "../gen/kernel/v1/kernel_pb";
import { fieldAccess } from "../model/typeinfo";
import { createFields, formValues, patchFields } from "../model/values";
import { FieldInput } from "./FieldInput";
import { RefusalView } from "./RefusalView";

export type ItemFormProps = {
  type: Type;
  // Absent on create.
  item?: Item;
  principals?: string[];
  // The roles the acting user holds, when known: an edit's patch leaves
  // out a field reserved to roles they do not hold (values.patchFields).
  // Such a field is read-only for them (typeinfo.fieldAccess).
  roles?: readonly string[];
  // The submit button's text: Create, Save.
  submitLabel: string;
  // Create: the filled fields (values.createFields). Edit: the changed
  // fields only, null for an emptied one (values.patchFields). A rejection
  // is shown as the server's refusal; the form keeps what was typed.
  onSubmit: (fields: JsonObject) => Promise<void>;
};

export function ItemForm({ type, item, principals, roles, submitLabel, onSubmit }: ItemFormProps): ReactElement {
  const [form, setForm] = useState(() => formValues(type, item?.fields));
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);

  const fields = item ? patchFields(type, item.fields, form, roles) : createFields(type, form);
  // An edit with nothing changed has nothing to save; a create may be empty.
  const nothingToSave = item !== undefined && Object.keys(fields).length === 0;

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await onSubmit(fields);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} className="item-form">
      {type.fields.map((f) => (
        <FieldInput
          key={f.id}
          field={f}
          value={form[f.id] ?? ""}
          access={fieldAccess(type, f.id, item?.state ?? "", roles)}
          state={item?.state}
          principals={principals}
          onChange={(v) => setForm((old) => ({ ...old, [f.id]: v }))}
        />
      ))}
      {type.fields.length === 0 && <p className="hint">This Type declares no fields.</p>}
      <button type="submit" disabled={busy || nothingToSave}>
        {submitLabel}
      </button>
      {error !== undefined && <RefusalView error={error} />}
    </form>
  );
}
