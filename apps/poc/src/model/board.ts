// The sprint board: columns by state, rank order inside each, and what a
// drop asks for. A drop inside a column is a rank move; across columns it
// is a transition, then a rank write next to a neighbour, only once the
// transition has succeeded.

export const COLUMNS = ["todo", "in_progress", "in_review", "done"] as const;
export type Column = (typeof COLUMNS)[number];

// A card: key "" when the issue has no rank yet.
export type Card = { name: string; container: string; key: string; state: string };

// The cards of each column, in the order given (rank order), and the
// canceled ones apart.
export function columnsOf(cards: Card[]): Record<Column, Card[]> & { canceled: Card[] } {
  const out = { todo: [], in_progress: [], in_review: [], done: [], canceled: [] } as Record<Column, Card[]> & { canceled: Card[] };
  for (const c of cards) {
    if ((COLUMNS as readonly string[]).includes(c.state)) out[c.state as Column].push(c);
    else if (c.state === "canceled") out.canceled.push(c);
  }
  return out;
}

export type Placement = { pos: "before" | "after"; other: string };
export type DropPlan =
  | { kind: "none" }
  | { kind: "rank"; pos: "before" | "after" | "bottom"; other?: string }
  | { kind: "transition"; to: string; then?: Placement }
  | { kind: "refused"; reason: "no_transition" | "other_container" };

// Where a card dropped between `above` and `below` is ranked: after the
// card above when it is ranked and in the same container, else before the
// card below on the same condition. "bottom" when neither is ranked;
// undefined when they are ranked but in another container.
function placement(moved: Card, above: Card | undefined, below: Card | undefined): Placement | "bottom" | undefined {
  const ranked = (c: Card | undefined) => c !== undefined && c.key !== "";
  const usable = (c: Card | undefined): c is Card => ranked(c) && c!.container === moved.container;
  if (usable(above)) return { pos: "after", other: above.name };
  if (usable(below)) return { pos: "before", other: below.name };
  if (!ranked(above) && !ranked(below)) return "bottom";
  return undefined;
}

// The card `card` dropped at `index` of column `to` (counted without the
// card). Inside its column: after the card above when that one is ranked
// and in the same container, else before the card below on the same
// condition, else the bottom when no neighbour is ranked, else refused.
// Into another column: refused unless the Type declares the move
// (declares), else a transition placed the same way when it can be.
export function planDrop(
  cards: Card[],
  card: string,
  to: Column,
  index: number,
  declares: (from: string, to: string) => boolean,
): DropPlan {
  const moved = cards.find((c) => c.name === card);
  if (!moved) return { kind: "none" };
  const column = columnsOf(cards)[to].filter((c) => c.name !== card);
  const above = column[index - 1];
  const below = column[index];
  if (moved.state === to) {
    const now = columnsOf(cards)[to].findIndex((c) => c.name === card);
    if (now === index) return { kind: "none" };
    const p = placement(moved, above, below);
    if (p === undefined) return { kind: "refused", reason: "other_container" };
    return p === "bottom" ? { kind: "rank", pos: "bottom" } : { kind: "rank", ...p };
  }
  if (!declares(moved.state, to)) return { kind: "refused", reason: "no_transition" };
  if (column.length === 0) return { kind: "transition", to };
  const p = placement(moved, above, below);
  return p && p !== "bottom" ? { kind: "transition", to, then: p } : { kind: "transition", to };
}

// What carrying out a plan needs.
export type DropDeps = {
  transition: (card: string, to: string, fields?: Record<string, string>) => Promise<void>;
  rank: (card: string, pos: "before" | "after" | "bottom", other?: string) => Promise<void>;
};

// Carries out a plan: a rank move; or the transition, and the rank write
// only after it succeeded. A refusal is thrown as it came.
export async function runDrop(deps: DropDeps, card: string, plan: DropPlan, fields?: Record<string, string>): Promise<void> {
  if (plan.kind === "rank") return deps.rank(card, plan.pos, plan.other);
  if (plan.kind !== "transition") return;
  await deps.transition(card, plan.to, fields);
  if (plan.then) await deps.rank(card, plan.then.pos, plan.then.other);
}

// Moving a card with the keyboard: Space (or Enter) picks the focused card
// up and drops it; arrows move it while held; Escape puts it back.
export type Holding = { card: string; column: Column; index: number };
export type KeyResult = { holding?: Holding; drop?: Holding; cancel?: boolean };
export type BoardKey = " " | "Enter" | "Escape" | "ArrowUp" | "ArrowDown" | "ArrowLeft" | "ArrowRight";
export function boardKey(holding: Holding | undefined, key: BoardKey, focused: string, columns: Record<Column, string[]>): KeyResult {
  // The places in a column for the held card: its cards without it, plus one.
  const last = (col: Column, card: string) => columns[col].filter((c) => c !== card).length;
  if (!holding) {
    if (key !== " " && key !== "Enter") return {};
    const column = COLUMNS.find((col) => columns[col].includes(focused));
    if (!column) return {};
    return { holding: { card: focused, column, index: columns[column].indexOf(focused) } };
  }
  switch (key) {
    case " ":
    case "Enter":
      return { drop: holding };
    case "Escape":
      return { cancel: true };
    case "ArrowUp":
      return { holding: { ...holding, index: Math.max(0, holding.index - 1) } };
    case "ArrowDown":
      return { holding: { ...holding, index: Math.min(last(holding.column, holding.card), holding.index + 1) } };
    case "ArrowLeft":
    case "ArrowRight": {
      const i = COLUMNS.indexOf(holding.column) + (key === "ArrowRight" ? 1 : -1);
      const column = COLUMNS[i];
      if (!column) return { holding };
      return { holding: { card: holding.card, column, index: Math.min(holding.index, last(column, holding.card)) } };
    }
  }
}
