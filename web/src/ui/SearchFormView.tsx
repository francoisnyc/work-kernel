// The filters of a search or a list, the same for every Type: words,
// Types, states, categories, field conditions and an order. What the
// server cannot do with them (a field two Types declare differently) is
// its refusal to show, not this form's to prevent.
import { useId, useState, type FormEvent, type ReactElement } from "react";
import { FieldType, StateCategory, type Type } from "../gen/kernel/v1/kernel_pb";
import type { FieldCondition, SearchForm } from "../model/search";
import { latestVersions } from "../model/typeinfo";
import { categoryName } from "./ItemList";

const CATEGORIES = [
  StateCategory.TRIAGE,
  StateCategory.BACKLOG,
  StateCategory.TODO,
  StateCategory.ACTIVE,
  StateCategory.WAITING,
  StateCategory.DONE,
  StateCategory.CANCELED,
  StateCategory.NONE,
];
const OPS: FieldCondition["op"][] = ["eq", "gt", "gte", "lt", "lte"];

const toggle = <T,>(list: T[], v: T) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);

export function SearchFormView(props: {
  initial: SearchForm;
  types: Type[];
  words: boolean;
  submitLabel: string;
  onSubmit: (form: SearchForm) => void;
}): ReactElement {
  const id = useId();
  const [form, setForm] = useState(props.initial);
  const [statesText, setStatesText] = useState(props.initial.states.join(", "));
  const set = (change: Partial<SearchForm>) => setForm((old) => ({ ...old, ...change }));

  // The fields and states to offer: those of the chosen Types (any
  // version), or of every Type when none is chosen.
  const inPlay = props.types.filter((t) => form.types.length === 0 || form.types.includes(t.typeId));
  const fieldTypes = new Map<string, FieldType>();
  const states = new Set<string>();
  for (const t of inPlay) {
    for (const f of t.fields) if (!fieldTypes.has(f.id)) fieldTypes.set(f.id, f.type);
    for (const s of t.states) states.add(s.id);
  }
  const fieldIds = [...fieldTypes.keys()].sort();

  function setCondition(i: number, change: Partial<FieldCondition>) {
    set({ fields: form.fields.map((c, j) => (j === i ? { ...c, ...change } : c)) });
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    props.onSubmit({
      ...form,
      states: statesText
        .split(",")
        .map((x) => x.trim())
        .filter((x) => x !== ""),
      // The type each condition's value is sent as, read from the Types.
      fields: form.fields
        .filter((c) => c.field !== "" && c.value !== "")
        .map((c) => ({ ...c, fieldType: fieldTypes.get(c.field) ?? FieldType.STRING })),
    });
  }

  return (
    <form onSubmit={submit} className="search-form">
      {props.words && (
        <div className="field">
          <label htmlFor={`${id}-words`}>Words</label>
          <input id={`${id}-words`} type="text" value={form.words} onChange={(e) => set({ words: e.target.value })} />
        </div>
      )}
      <fieldset>
        <legend>Types</legend>
        {latestVersions(props.types).map((t) => (
          <label key={t.typeId} className="check">
            <input
              type="checkbox"
              checked={form.types.includes(t.typeId)}
              onChange={() => set({ types: toggle(form.types, t.typeId) })}
            />
            {t.typeId}
          </label>
        ))}
      </fieldset>
      <fieldset>
        <legend>Categories</legend>
        {CATEGORIES.map((c) => (
          <label key={c} className="check">
            <input
              type="checkbox"
              checked={form.categories.includes(c)}
              onChange={() => set({ categories: toggle(form.categories, c) })}
            />
            {categoryName(c)}
          </label>
        ))}
      </fieldset>
      <div className="field">
        <label htmlFor={`${id}-states`}>States (comma-separated)</label>
        <input
          id={`${id}-states`}
          type="text"
          placeholder={[...states].sort().slice(0, 4).join(", ")}
          value={statesText}
          onChange={(e) => setStatesText(e.target.value)}
        />
      </div>
      <fieldset>
        <legend>Field conditions</legend>
        {form.fields.map((c, i) => (
          <div key={i} className="condition">
            <select aria-label="Field" value={c.field} onChange={(e) => setCondition(i, { field: e.target.value })}>
              <option value="">(field)</option>
              {fieldIds.map((f) => (
                <option key={f} value={f}>
                  {f}
                </option>
              ))}
            </select>
            <select
              aria-label="Operator"
              value={c.op}
              onChange={(e) => setCondition(i, { op: e.target.value as FieldCondition["op"] })}
            >
              {OPS.map((op) => (
                <option key={op} value={op}>
                  {op}
                </option>
              ))}
            </select>
            <input
              aria-label="Value"
              type="text"
              placeholder={fieldTypes.get(c.field) === FieldType.PRINCIPAL ? "me, or user:id" : "value"}
              value={c.value}
              onChange={(e) => setCondition(i, { value: e.target.value })}
            />
            <button type="button" className="quiet" onClick={() => set({ fields: form.fields.filter((_, j) => j !== i) })}>
              Remove condition
            </button>
          </div>
        ))}
        <button
          type="button"
          className="quiet"
          onClick={() => set({ fields: [...form.fields, { field: "", op: "eq", value: "", fieldType: FieldType.STRING }] })}
        >
          Add a condition
        </button>
      </fieldset>
      <div className="field">
        <label htmlFor={`${id}-order`}>Order by</label>
        <select id={`${id}-order`} value={form.orderBy} onChange={(e) => set({ orderBy: e.target.value })}>
          <option value="">newest first</option>
          {fieldIds.map((f) => (
            <option key={f} value={f}>
              {f}
            </option>
          ))}
        </select>
        <label className="check">
          <input type="checkbox" checked={form.desc} onChange={(e) => set({ desc: e.target.checked })} />
          descending
        </label>
      </div>
      {form.linked && (
        <p className="chip">
          linked by {form.linked.linkType} to {form.linked.item.split("/").pop()?.slice(0, 8)}{" "}
          <button type="button" className="quiet" onClick={() => set({ linked: undefined })}>
            Remove link filter
          </button>
        </p>
      )}
      <button type="submit">{props.submitLabel}</button>
    </form>
  );
}
