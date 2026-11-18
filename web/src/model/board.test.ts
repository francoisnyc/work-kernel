import { describe, expect, test } from "vitest";
import { dropToMove, type BoardCard } from "./board";

const card = (name: string, key: string, container = "plat"): BoardCard => ({ name, container, key });
const a = card("a", "F");
const b = card("b", "K");
const c = card("c", "P");
const d = card("d", "U");

describe("dropToMove", () => {
  test("dropped where it was: nothing to do", () => {
    expect(dropToMove([a, b, c], 1, 1)).toEqual({ kind: "none" });
  });

  test("goes after the card above", () => {
    // c dropped between a and b.
    expect(dropToMove([a, b, c], 2, 1)).toEqual({ kind: "move", pos: "after", other: a });
    // a dropped at the end.
    expect(dropToMove([a, b, c], 0, 2)).toEqual({ kind: "move", pos: "after", other: c });
  });

  test("at the top of the board: before the first card, not the top of the container", () => {
    expect(dropToMove([a, b, c], 2, 0)).toEqual({ kind: "move", pos: "before", other: a });
  });

  test("the card above is in another container: before the card below", () => {
    const x = card("x", "H", "other");
    // d dropped between x and b.
    expect(dropToMove([a, x, b, d], 3, 2)).toEqual({ kind: "move", pos: "before", other: b });
  });

  test("both neighbours are in other containers: refused, a rank orders one container", () => {
    const x = card("x", "H", "other");
    const y = card("y", "I", "other");
    expect(dropToMove([a, x, y, d], 3, 2)).toEqual({ kind: "refused", reason: "other_container" });
    // The only neighbour is in another container.
    expect(dropToMove([x, a], 1, 0)).toEqual({ kind: "refused", reason: "other_container" });
  });

  test("an unranked card dropped into the ranked list is ranked next to its neighbour", () => {
    const n = card("n", "");
    expect(dropToMove([a, b, n], 2, 1)).toEqual({ kind: "move", pos: "after", other: a });
  });

  test("an unranked neighbour names no place: the ranked one on the other side is used", () => {
    const n = card("n", "");
    // a dropped between c (ranked) and n (unranked): after c.
    expect(dropToMove([a, c, n], 0, 1)).toEqual({ kind: "move", pos: "after", other: c });
  });

  test("no ranked neighbour: the bottom of the card's container", () => {
    const n = card("n", "");
    const m = card("m", "");
    // a dropped after the unranked cards.
    expect(dropToMove([a, n, m], 0, 2)).toEqual({ kind: "move", pos: "bottom" });
    // The first card ever ranked on a board of unranked cards.
    expect(dropToMove([n, m], 1, 0)).toEqual({ kind: "move", pos: "bottom" });
  });
});
