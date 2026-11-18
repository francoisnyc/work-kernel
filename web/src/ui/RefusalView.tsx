// The server's refusal, shown as it came: code, reason, metadata.
//
// Phase B: signature only.
import type { ReactElement } from "react";

// error: whatever a call threw (refusalOf reads it). Renders an element
// with role="alert".
export function RefusalView(_props: { error: unknown }): ReactElement {
  throw new Error("not implemented");
}
