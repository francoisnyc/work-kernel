// An Item's moves as buttons. A move that requires fields asks for them in
// a dialog first (enum fields: their options). The server's refusal is
// shown in plain words beside the buttons.
import { useState, type ReactElement } from "react";
import { fieldLabel, optionLabel } from "../model/labels";
import type { Move } from "../model/moves";
import type { ExplainContext } from "../model/refusal";
import { Dialog } from "./Dialog";
import { RefusalNotice } from "./RefusalNotice";

export type MoveButtonsProps = {
  moves: Move[];
  // The options of the enum fields a move may require.
  options: Record<string, string[]>;
  onMove: (to: string, fields?: Record<string, string>) => Promise<void>;
  ctx?: ExplainContext;
};

export function MoveButtons({ moves, options, onMove, ctx }: MoveButtonsProps): ReactElement {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const [asking, setAsking] = useState<Move>();

  async function go(to: string, fields?: Record<string, string>) {
    setBusy(true);
    setError(undefined);
    try {
      await onMove(to, fields);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="moves">
      {moves.length === 0 ? (
        <p className="muted">No moves from here.</p>
      ) : (
        <div className="button-row">
          {moves.map((m, i) => (
            <button
              key={m.to}
              type="button"
              className={i === 0 ? "button button-primary" : "button"}
              disabled={busy}
              onClick={() => (m.required.length > 0 ? setAsking(m) : void go(m.to))}
            >
              {m.label}
            </button>
          ))}
        </div>
      )}
      <RefusalNotice error={error} ctx={ctx} />
      {asking && (
        <RequiredDialog
          move={asking}
          options={options}
          onCancel={() => setAsking(undefined)}
          onConfirm={(fields) => {
            setAsking(undefined);
            void go(asking.to, fields);
          }}
        />
      )}
    </div>
  );
}

function RequiredDialog({
  move,
  options,
  onCancel,
  onConfirm,
}: {
  move: Move;
  options: Record<string, string[]>;
  onCancel: () => void;
  onConfirm: (fields: Record<string, string>) => void;
}): ReactElement {
  const [values, setValues] = useState<Record<string, string>>({});
  const ready = move.required.every((f) => (values[f] ?? "").trim() !== "");
  return (
    <Dialog title={move.label} onClose={onCancel}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (ready) onConfirm(values);
        }}
      >
        {move.required.map((f) => {
          const id = `required-${f}`;
          const opts = options[f];
          return (
            <div className="field" key={f}>
              <label htmlFor={id}>{fieldLabel(f)}</label>
              {opts ? (
                <select id={id} value={values[f] ?? ""} onChange={(e) => setValues({ ...values, [f]: e.target.value })}>
                  <option value="">Choose…</option>
                  {opts.map((o) => (
                    <option key={o} value={o}>
                      {optionLabel(f, o)}
                    </option>
                  ))}
                </select>
              ) : (
                <input id={id} value={values[f] ?? ""} onChange={(e) => setValues({ ...values, [f]: e.target.value })} />
              )}
            </div>
          );
        })}
        <div className="button-row dialog-actions">
          <button type="button" className="button" onClick={onCancel}>
            Cancel
          </button>
          <button type="submit" className="button button-primary" disabled={!ready}>
            {move.label}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
