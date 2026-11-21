// An Item's history in sentences. The kernel's own records are hidden
// unless asked for.
import type { ItemEvent, ItemRef } from "@gen/kernel/v1/kernel_pb";
import { useId, useState, type ReactElement } from "react";
import type { Audience } from "../model/people";
import { describeEvent } from "../model/timeline";
import { when } from "./format";

export function History({
  events,
  typeId,
  audience,
  titleOf,
}: {
  events: ItemEvent[];
  typeId: string;
  audience: Audience;
  titleOf: (ref: ItemRef) => string;
}): ReactElement {
  const [system, setSystem] = useState(false);
  const id = useId();
  const entries = events
    .map((e) => describeEvent(e, { typeId, audience, titleOf }))
    .filter((e) => e !== null)
    .filter((e) => system || !e.system);
  return (
    <section className="section" aria-labelledby={`${id}-title`}>
      <div className="section-title">
        <h2 id={`${id}-title`}>History</h2>
        <label className="muted">
          <input type="checkbox" checked={system} onChange={(e) => setSystem(e.target.checked)} /> Show automations
        </label>
      </div>
      <ol className="plain history">
        {entries.map((e, i) => (
          <li key={i} className={e.system ? "history-system" : undefined}>
            <span>
              <strong>{e.actor}</strong> {e.text}
            </span>
            <time className="history-time" dateTime={e.at}>
              {when(e.at)}
            </time>
          </li>
        ))}
      </ol>
    </section>
  );
}
