// A refusal or a failure, in plain words, with the server's answer (code,
// reason, metadata) behind "Details".
import type { ReactElement } from "react";
import type { ExplainContext } from "../model/refusal";

export function RefusalNotice(_props: { error: unknown; ctx?: ExplainContext }): ReactElement | null {
  return null;
}
