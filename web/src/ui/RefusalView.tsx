// The server's refusal, shown as it came: code, reason, metadata.
import type { ReactElement } from "react";
import { refusalOf } from "../model/refusal";

// error: whatever a call threw (refusalOf reads it). Renders an element
// with role="alert".
export function RefusalView({ error }: { error: unknown }): ReactElement {
  const r = refusalOf(error);
  return (
    <div role="alert" className="refusal">
      <p>
        <strong>{r.code}</strong> {r.reason}
      </p>
      <p>{r.message}</p>
      {(Object.keys(r.metadata).length > 0 || r.violations.length > 0) && (
        <ul>
          {Object.entries(r.metadata).map(([k, v]) => (
            <li key={k}>
              {k}: {v}
            </li>
          ))}
          {r.violations.map((v) => (
            <li key={v.field}>
              {v.field}: {v.description}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
