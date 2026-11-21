// The incident's composer: a public reply to the customer, or an internal
// note, never confused. Two tabs; the tab changes the colour, the label,
// the hint and the button.
import type { ReactElement } from "react";

export type ComposerProps = {
  // Who will read a reply: the requester's name ("kim").
  requester: string;
  onSend: (kind: "reply" | "note", body: string) => Promise<void>;
};

export function Composer(_props: ComposerProps): ReactElement | null {
  return null;
}
