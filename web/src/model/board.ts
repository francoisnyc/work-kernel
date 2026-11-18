// A drop on a board, as the rank move it asks for. A board shows the Items
// linked to one Item, in rank order; a rank orders one container, so the
// move is made next to a neighbour in the moved card's own container
// (rank.plan then ranks against that container).

// A card as the board shows it: key "" when the Item has no rank yet.
// Ranked cards come first, in key order; unranked ones last.
export type BoardCard = { name: string; container: string; key: string };

export type Drop =
  // Dropped where it was.
  | { kind: "none" }
  // other is set for before and after.
  | { kind: "move"; pos: "before" | "after" | "bottom"; other?: BoardCard }
  // Both neighbours are ranked cards of other containers.
  | { kind: "refused"; reason: "other_container" };

// The card at index `from` is dropped so that it ends at index `to` of the
// list (counted once the card is taken out). After the card above when
// that one is ranked and in the same container; else before the card
// below, on the same condition; when no neighbour is ranked, the bottom of
// its container.
export function dropToMove(cards: BoardCard[], from: number, to: number): Drop {
  const moved = cards[from];
  if (!moved || to === from) return { kind: "none" };
  const rest = cards.filter((_, i) => i !== from);
  const above = rest[to - 1];
  const below = rest[to];
  const ranked = (c: BoardCard | undefined) => c !== undefined && c.key !== "";
  const usable = (c: BoardCard | undefined): c is BoardCard => ranked(c) && c!.container === moved.container;
  if (usable(above)) return { kind: "move", pos: "after", other: above };
  if (usable(below)) return { kind: "move", pos: "before", other: below };
  if (!ranked(above) && !ranked(below)) return { kind: "move", pos: "bottom" };
  return { kind: "refused", reason: "other_container" };
}
