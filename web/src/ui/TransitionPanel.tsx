// The moves an Item's Type declares from its current state, as buttons.
// Nothing is evaluated here: every transition is offered, its rules are
// shown as text, and the server's answer (an outcome, or a refusal) is
// shown as it came.
import type { JsonObject } from "@bufbuild/protobuf";
import { useState, type FormEvent, type ReactElement } from "react";
import {
  TransitionOutcome,
  type Item,
  type Transition,
  type TransitionItemResponse,
  type Type,
} from "../gen/kernel/v1/kernel_pb";
import { fieldAccess, isSystemOnly, transitionsFrom, type TransitionView } from "../model/typeinfo";
import { formValues, patchFields, type FormValues } from "../model/values";
import { FieldInput } from "./FieldInput";
import { RefusalView } from "./RefusalView";

export type TransitionPanelProps = {
  type: Type;
  item: Item;
  principals?: string[];
  // Only these transitions (the inbox's rows); default: all leaving the
  // Item's state.
  only?: Transition[];
  // fields: the patch of the transition's required fields, changed ones
  // only. requestId: new for each move asked; the same again when the user
  // asks again about a PENDING one.
  onTransition: (toState: string, fields: JsonObject, requestId: string) => Promise<TransitionItemResponse>;
};

// A move that was asked, and the server's answer to it.
type Asked = { toState: string; fields: JsonObject; requestId: string; answer: TransitionItemResponse };

// The transition's rules, as the Type states them.
function rules(t: Transition): string[] {
  const out: string[] = [];
  if (t.allowed.length > 0) out.push(`allowed: ${t.allowed.join(", ")}`);
  if (t.except.length > 0) out.push(`except: ${t.except.join(", ")}`);
  if (t.guard !== "") out.push(`guard: ${t.guard}`);
  if (t.required.length > 0) out.push(`required: ${t.required.join(", ")}`);
  if (t.clears.length > 0) out.push(`clears: ${t.clears.join(", ")}`);
  if (t.checked) out.push("a check runs before the move");
  return out;
}

export function TransitionPanel({ type, item, principals, only, onTransition }: TransitionPanelProps): ReactElement {
  const views: TransitionView[] = only
    ? only.map((t) => ({ transition: t, systemOnly: isSystemOnly(t) }))
    : transitionsFrom(type, item.state);
  // The move whose required fields are being asked for, and their inputs.
  const [asking, setAsking] = useState<Transition>();
  const [form, setForm] = useState<FormValues>({});
  const [asked, setAsked] = useState<Asked>();
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);

  async function move(toState: string, fields: JsonObject, requestId: string) {
    setBusy(true);
    setError(undefined);
    setAsked(undefined);
    try {
      const answer = await onTransition(toState, fields, requestId);
      setAsked({ toState, fields, requestId, answer });
      setAsking(undefined);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  }

  function start(t: Transition) {
    if (t.required.length === 0) {
      void move(t.toState, {}, crypto.randomUUID());
      return;
    }
    setForm(formValues(type, item.fields));
    setAsking(t);
  }

  function confirm(e: FormEvent) {
    e.preventDefault();
    if (!asking) return;
    // The changed ones among the required fields; whether the rest are
    // set is the server's to judge.
    const patch = patchFields(type, item.fields, form);
    const fields: JsonObject = {};
    for (const id of asking.required) {
      if (id in patch) fields[id] = patch[id]!;
    }
    void move(asking.toState, fields, crypto.randomUUID());
  }

  const outcome = asked ? TransitionOutcome[asked.answer.outcome] : "";
  return (
    <div className="transitions">
      {item.hold && (
        <p className="hold">
          Held: check {item.hold.check} is running, waiting to move to {item.hold.requestedState}
        </p>
      )}
      {views.length === 0 && <p className="hint">No transition leaves {item.state}.</p>}
      <ul>
        {views.map(({ transition: t, systemOnly }) => (
          <li key={`${t.fromState}>${t.toState}`}>
            <button type="button" disabled={systemOnly || busy} onClick={() => start(t)}>
              Move to {t.toState}
            </button>
            {systemOnly && <span className="hint"> only the system can do this</span>}
            {rules(t).map((r) => (
              <span key={r} className="rule">
                {r}
              </span>
            ))}
          </li>
        ))}
      </ul>
      {asking && (
        <form onSubmit={confirm} className="item-form">
          {type.fields
            .filter((f) => asking.required.includes(f.id))
            .map((f) => (
              <FieldInput
                key={f.id}
                field={f}
                value={form[f.id] ?? ""}
                // The transition's own patch may set what its states freeze.
                access={fieldAccess(type, f.id, "")}
                principals={principals}
                onChange={(v) => setForm((old) => ({ ...old, [f.id]: v }))}
              />
            ))}
          <button type="submit" disabled={busy}>
            Confirm move to {asking.toState}
          </button>
          <button type="button" className="quiet" onClick={() => setAsking(undefined)}>
            Cancel
          </button>
        </form>
      )}
      {asked && (
        <p role="status" className={`outcome outcome-${outcome.toLowerCase()}`}>
          Move to {asked.toState}: <strong>{outcome}</strong> {asked.answer.reason}
        </p>
      )}
      {asked?.answer.outcome === TransitionOutcome.PENDING && (
        <button type="button" disabled={busy} onClick={() => move(asked.toState, asked.fields, asked.requestId)}>
          Check again
        </button>
      )}
      {error !== undefined && <RefusalView error={error} />}
    </div>
  );
}
