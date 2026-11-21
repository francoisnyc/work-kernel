// The sprint board: columns by state, rank order inside each, and what a
// drop asks for. A drop inside a column is a rank move; across columns it
// is a transition, then a rank write next to a neighbour, only once the
// transition has succeeded.
import { todo } from "./todo";

export const COLUMNS = ["todo", "in_progress", "in_review", "done"] as const;
export type Column = (typeof COLUMNS)[number];

// A card: key "" when the issue has no rank yet.
export type Card = { name: string; container: string; key: string; state: string };

// The cards of each column, in the order given (rank order), and the
// canceled ones apart.
export function columnsOf(_cards: Card[]): Record<Column, Card[]> & { canceled: Card[] } {
  return todo();
}

export type Placement = { pos: "before" | "after"; other: string };
export type DropPlan =
  | { kind: "none" }
  | { kind: "rank"; pos: "before" | "after" | "bottom"; other?: string }
  | { kind: "transition"; to: string; then?: Placement }
  | { kind: "refused"; reason: "no_transition" | "other_container" };

// The card `card` dropped at `index` of column `to` (counted without the
// card). Inside its column: after the card above when that one is ranked
// and in the same container, else before the card below on the same
// condition, else the bottom when no neighbour is ranked, else refused.
// Into another column: refused unless the Type declares the move
// (declares), else a transition placed the same way when it can be.
export function planDrop(
  _cards: Card[],
  _card: string,
  _to: Column,
  _index: number,
  _declares: (from: string, to: string) => boolean,
): DropPlan {
  return todo();
}

// What carrying out a plan needs.
export type DropDeps = {
  transition: (card: string, to: string, fields?: Record<string, string>) => Promise<void>;
  rank: (card: string, pos: "before" | "after" | "bottom", other?: string) => Promise<void>;
};

// Carries out a plan: a rank move; or the transition, and the rank write
// only after it succeeded. A refusal is thrown as it came.
export async function runDrop(_deps: DropDeps, _card: string, _plan: DropPlan, _fields?: Record<string, string>): Promise<void> {
  return todo();
}

// Moving a card with the keyboard: Space (or Enter) picks the focused card
// up and drops it; arrows move it while held; Escape puts it back.
export type Holding = { card: string; column: Column; index: number };
export type KeyResult = { holding?: Holding; drop?: Holding; cancel?: boolean };
export type BoardKey = " " | "Enter" | "Escape" | "ArrowUp" | "ArrowDown" | "ArrowLeft" | "ArrowRight";
export function boardKey(
  _holding: Holding | undefined,
  _key: BoardKey,
  _focused: string,
  _columns: Record<Column, string[]>,
): KeyResult {
  return todo();
}
