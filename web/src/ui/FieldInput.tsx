// One field's input, chosen by the field's declared type: never by Type.
import { useId, type ReactElement } from "react";
import { FieldType, type Field } from "../gen/kernel/v1/kernel_pb";
import type { FieldAccess } from "../model/typeinfo";
import { instantOf } from "../model/values";

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
      input = (
        <input
          {...common}
          type="datetime-local"
          step="1"
          title={utc === "" ? undefined : utc}
          value={text}
          onChange={(e) => onChange(e.target.value)}
        />
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
