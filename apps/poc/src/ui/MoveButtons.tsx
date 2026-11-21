// An Item's moves as buttons. A move that requires fields asks for them in
// a dialog first (enum fields: their options). The server's refusal is
// shown in plain words beside the buttons.
import type { ReactElement } from "react";
import type { Move } from "../model/moves";
import type { ExplainContext } from "../model/refusal";

export type MoveButtonsProps = {
  moves: Move[];
  // The options of the enum fields a move may require.
  options: Record<string, string[]>;
  onMove: (to: string, fields?: Record<string, string>) => Promise<void>;
  ctx?: ExplainContext;
};

export function MoveButtons(_props: MoveButtonsProps): ReactElement | null {
  return null;
}
