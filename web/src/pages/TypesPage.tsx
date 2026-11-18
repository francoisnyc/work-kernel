// The Types the server loaded, read-only, as the API describes them:
// everything the generic UI is built from.
import { toJson } from "@bufbuild/protobuf";
import type { ReactElement } from "react";
import { useSession } from "../app/session";
import { TypeSchema } from "../gen/kernel/v1/kernel_pb";

export function TypesPage(): ReactElement {
  const session = useSession();
  return (
    <>
      <h1>Types</h1>
      <p className="hint">Read-only: Types load from files when the item service starts.</p>
      {session.types.map((t) => (
        <details key={t.name}>
          <summary>
            {t.typeId}@{t.version}{" "}
            <span className="meta">
              {t.fields.length} fields, {t.states.length} states, {t.transitions.length} transitions, {t.linkTypes.length} link
              types
            </span>
          </summary>
          <pre>{JSON.stringify(toJson(TypeSchema, t), null, 2)}</pre>
        </details>
      ))}
    </>
  );
}
