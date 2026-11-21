// The incident's composer: a public reply to the customer, or an internal
// note, never confused. Two tabs; the tab changes the colour, the label,
// the hint and the button.
import { useId, useRef, useState, type KeyboardEvent, type ReactElement } from "react";
import { Icon } from "./Icon";
import { RefusalNotice } from "./RefusalNotice";

export type ComposerProps = {
  // Who will read a reply: the requester's name ("kim").
  requester: string;
  onSend: (kind: "reply" | "note", body: string) => Promise<void>;
};

const kinds = ["reply", "note"] as const;

export function Composer({ requester, onSend }: ComposerProps): ReactElement {
  const [kind, setKind] = useState<"reply" | "note">("reply");
  const [bodies, setBodies] = useState({ reply: "", note: "" });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  const id = useId();
  const tabs = useRef<(HTMLButtonElement | null)[]>([]);
  const body = bodies[kind];
  const label = kind === "reply" ? "Reply" : "Internal note";

  function onTabKey(e: KeyboardEvent) {
    if (e.key !== "ArrowRight" && e.key !== "ArrowLeft") return;
    e.preventDefault();
    const next = kind === "reply" ? "note" : "reply";
    setKind(next);
    tabs.current[kinds.indexOf(next)]?.focus();
  }

  async function send() {
    setBusy(true);
    setError(undefined);
    try {
      await onSend(kind, body);
      setBodies((b) => ({ ...b, [kind]: "" }));
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={`composer composer-${kind}`}>
      <div role="tablist" aria-label="Message type" className="composer-tabs">
        {kinds.map((k, i) => (
          <button
            key={k}
            ref={(el) => {
              tabs.current[i] = el;
            }}
            type="button"
            role="tab"
            id={`${id}-tab-${k}`}
            aria-selected={kind === k}
            aria-controls={`${id}-panel`}
            tabIndex={kind === k ? 0 : -1}
            className="composer-tab"
            onClick={() => setKind(k)}
            onKeyDown={onTabKey}
          >
            {k === "note" && <Icon name="lock" />}
            {k === "reply" ? "Reply to customer" : "Internal note"}
          </button>
        ))}
      </div>
      <form
        role="tabpanel"
        id={`${id}-panel`}
        aria-labelledby={`${id}-tab-${kind}`}
        className="composer-panel"
        onSubmit={(e) => {
          e.preventDefault();
          if (body.trim() !== "" && !busy) void send();
        }}
      >
        <label htmlFor={`${id}-body`} className="sr-only">
          {label}
        </label>
        <textarea
          id={`${id}-body`}
          rows={4}
          value={body}
          placeholder={kind === "reply" ? `Write to ${requester}…` : "Write a note for the team…"}
          onChange={(e) => setBodies((b) => ({ ...b, [kind]: e.target.value }))}
        />
        <div className="composer-foot">
          <p className="composer-hint">
            {kind === "reply" ? (
              <>
                <Icon name="user" />
                {requester} will see this
              </>
            ) : (
              <>
                <Icon name="lock" />
                Only staff can see this
              </>
            )}
          </p>
          <button type="submit" className="button button-primary" disabled={body.trim() === "" || busy}>
            {kind === "reply" ? "Send reply" : "Add note"}
          </button>
        </div>
        <RefusalNotice error={error} />
      </form>
    </div>
  );
}
