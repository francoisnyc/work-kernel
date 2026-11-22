// A refusal or a failure, in plain words, with the server's answer (code,
// reason, metadata) behind "Details".
import type { ReactElement } from "react";
import { explain, refusalOf, type ExplainContext } from "../model/refusal";
import { Icon } from "./Icon";

// text: a screen's own sentence for this refusal (the Requests area's),
// in place of the apps' usual one; the details stay the server's answer.
export function RefusalNotice({ error, ctx, text }: { error: unknown; ctx?: ExplainContext; text?: string }): ReactElement | null {
  if (error === undefined || error === null) return null;
  const r = refusalOf(error);
  return (
    <div className="notice notice-danger" role="alert">
      <p className="notice-text">
        <Icon name="alert" />
        <span>{text ?? explain(r, ctx)}</span>
      </p>
      <details className="notice-details">
        <summary>Details</summary>
        <dl>
          <dt>Code</dt>
          <dd>{r.code}</dd>
          {r.reason !== "" && (
            <>
              <dt>Reason</dt>
              <dd>{r.reason}</dd>
            </>
          )}
          {r.message !== "" && (
            <>
              <dt>Message</dt>
              <dd>{r.message}</dd>
            </>
          )}
        </dl>
        {Object.keys(r.metadata).length > 0 && (
          <ul className="plain">
            {Object.entries(r.metadata).map(([k, v]) => (
              <li key={k}>
                <code>
                  {k}: {v}
                </code>
              </li>
            ))}
          </ul>
        )}
        {r.violations.length > 0 && (
          <ul className="plain">
            {r.violations.map((v) => (
              <li key={v.field}>
                <code>{v.field}</code>: {v.description}
              </li>
            ))}
          </ul>
        )}
      </details>
    </div>
  );
}
