// One field's input, chosen by the field's declared type: never by Type.
import { useId, useState, type KeyboardEvent, type ReactElement } from "react";
import { FieldType, type Field } from "../gen/kernel/v1/kernel_pb";
import type { FieldAccess } from "../model/typeinfo";
import { instantOf, type FormValue } from "../model/values";
import { relativeTime } from "../model/reltime";

export type FieldInputProps = {
  field: Field;
  // Text, a boolean for a bool field, or the members of a multi-value field.
  value: FormValue;
  onChange: (value: FormValue) => void;
  access: FieldAccess;
  // The Item's state, to say where a frozen field is frozen.
  state?: string;
  // Suggestions for a principal field (the dev users).
  principals?: string[];
};

// Why the field is read-only, or who it is reserved to; "" for an
// ordinary field.
function hintOf(access: FieldAccess, state: string | undefined): string {
  if (access.mode === "kernel") return "written by the kernel";
  if (access.mode === "frozen") return `frozen in state ${state ?? ""}`;
  if (access.roles.length > 0) return `written by role ${access.roles.join(", ")}`;
  return "";
}

// The input is labelled with the field's ID. A kernel-written or frozen
// field is disabled; why (and a role-written field's roles) is the
// input's accessible description.
export function FieldInput({ field, value, onChange, access, state, principals }: FieldInputProps): ReactElement {
  const id = useId();
  const hint = hintOf(access, state);
  const common = {
    id,
    disabled: access.mode !== "editable",
    "aria-describedby": hint ? `${id}-hint` : undefined,
  };
  const text = typeof value === "string" ? value : "";
  if (field.multiple) {
    return (
      <SetInput
        field={field}
        members={Array.isArray(value) ? value : []}
        onChange={onChange}
        editable={access.mode === "editable"}
        hint={hint}
        principals={principals}
      />
    );
  }

  let input: ReactElement;
  switch (field.type) {
    case FieldType.BOOL:
      input = <input {...common} type="checkbox" checked={value === true} onChange={(e) => onChange(e.target.checked)} />;
      break;
    case FieldType.ENUM:
      input = (
        <select {...common} value={text} onChange={(e) => onChange(e.target.value)}>
          <option value="">(not set)</option>
          {field.options.map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </select>
      );
      break;
    case FieldType.DATE:
      input = <input {...common} type="date" value={text} onChange={(e) => onChange(e.target.value)} />;
      break;
    case FieldType.DATETIME: {
      // The form holds the instant on the browser's clock, to the second;
      // the title says which instant that is in UTC.
      const utc = instantOf(text);
      // How far away it is, on the wall clock (and labelled so: the
      // instant may have been computed in working time on a calendar).
      const away = relativeTime(utc, new Date());
      input = (
        <>
          <input
            {...common}
            type="datetime-local"
            step="1"
            title={utc === "" ? undefined : utc}
            value={text}
            onChange={(e) => onChange(e.target.value)}
          />
          {away !== "" && <span className="meta relative-time"> {away}</span>}
        </>
      );
      break;
    }
    case FieldType.INT:
      // Text, not type=number: an int64 does not fit a JS number.
      input = <input {...common} type="text" inputMode="numeric" value={text} onChange={(e) => onChange(e.target.value)} />;
      break;
    case FieldType.PRINCIPAL:
      input = (
        <>
          <input
            {...common}
            type="text"
            list={`${id}-principals`}
            placeholder="user:id"
            value={text}
            onChange={(e) => onChange(e.target.value)}
          />
          <datalist id={`${id}-principals`}>
            {(principals ?? []).map((p) => (
              <option key={p} value={p} />
            ))}
          </datalist>
        </>
      );
      break;
    default:
      input = <input {...common} type="text" value={text} onChange={(e) => onChange(e.target.value)} />;
  }
  return (
    <div className="field">
      <label htmlFor={id}>{field.id}</label>
      {input}
      {hint && (
        <span id={`${id}-hint`} className="hint">
          {hint}
        </span>
      )}
    </div>
  );
}

// A multi-value field: its members as chips, each with a remove button,
// and one input that adds a member (a select for an enum, a date input for
// dates). The form holds the members sorted, as the server stores them;
// what is sent is a set operation (model/values.ts).
function SetInput(props: {
  field: Field;
  members: string[];
  onChange: (value: FormValue) => void;
  editable: boolean;
  hint: string;
  principals?: string[];
}): ReactElement {
  const { field, members, onChange, editable, hint, principals } = props;
  const id = useId();
  const [draft, setDraft] = useState("");
  const full = field.max > 0 && members.length >= field.max;
  const label = `Add to ${field.id}`;

  const add = () => {
    const member = field.type === FieldType.STRING ? draft : draft.trim();
    setDraft("");
    if (member === "" || members.includes(member) || full) return;
    onChange([...members, member].sort());
  };
  // Enter adds a member; it must not submit the form around the field.
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key !== "Enter") return;
    e.preventDefault();
    add();
  };
  const common = {
    "aria-label": label,
    disabled: !editable,
    value: draft,
    onKeyDown,
  };

  let input: ReactElement;
  switch (field.type) {
    case FieldType.ENUM:
      input = (
        <select {...common} onChange={(e) => setDraft(e.target.value)}>
          <option value="">(choose)</option>
          {field.options
            .filter((o) => !members.includes(o))
            .map((o) => (
              <option key={o} value={o}>
                {o}
              </option>
            ))}
        </select>
      );
      break;
    case FieldType.DATE:
      input = <input {...common} type="date" onChange={(e) => setDraft(e.target.value)} />;
      break;
    case FieldType.PRINCIPAL:
      input = (
        <>
          <input {...common} type="text" list={`${id}-principals`} placeholder="user:id" onChange={(e) => setDraft(e.target.value)} />
          <datalist id={`${id}-principals`}>
            {(principals ?? []).map((p) => (
              <option key={p} value={p} />
            ))}
          </datalist>
        </>
      );
      break;
    default:
      input = <input {...common} type="text" onChange={(e) => setDraft(e.target.value)} />;
  }
  return (
    <div className="field" role="group" aria-label={field.id}>
      <span className="label">{field.id}</span>
      {members.length === 0 ? (
        <span className="hint">(none)</span>
      ) : (
        <ul className="chips">
          {members.map((m) => (
            <li key={m}>
              {m}
              {editable && (
                <button type="button" aria-label={`Remove ${m}`} onClick={() => onChange(members.filter((x) => x !== m))}>
                  Remove
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      {input}
      <button type="button" disabled={!editable || full} onClick={add}>
        {label}
      </button>
      {field.max > 0 && <span className="hint">at most {field.max}</span>}
      {hint && <span className="hint">{hint}</span>}
    </div>
  );
}
