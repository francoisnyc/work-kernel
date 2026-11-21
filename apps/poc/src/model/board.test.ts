// The sprint board's drops. A rank write is only ever made after the
// transition it follows has succeeded.
import { describe, expect, test, vi } from "vitest";
import { storyType } from "../test/fixtures";
import { boardKey, columnsOf, planDrop, runDrop, type Card, type Column } from "./board";
import { declaresMove } from "./moves";

const card = (name: string, state: string, key: string, container = "plat"): Card => ({ name, state, key, container });
const declares = (from: string, to: string) => declaresMove(storyType, from, to);

// Rank order: A, B, C in To do; D in In progress; E done; X canceled.
const cards = [
  card("A", "todo", "a"),
  card("D", "in_progress", "b"),
  card("B", "todo", "c"),
  card("X", "canceled", "d"),
  card("C", "todo", "e"),
  card("E", "done", "f"),
];

test("columns by state, in rank order, canceled apart", () => {
  const cols = columnsOf(cards);
  expect(cols.todo.map((c) => c.name)).toEqual(["A", "B", "C"]);
  expect(cols.in_progress.map((c) => c.name)).toEqual(["D"]);
  expect(cols.in_review).toEqual([]);
  expect(cols.done.map((c) => c.name)).toEqual(["E"]);
  expect(cols.canceled.map((c) => c.name)).toEqual(["X"]);
});

describe("planDrop inside a column", () => {
  test("to the top: before the first card", () => {
    expect(planDrop(cards, "C", "todo", 0, declares)).toEqual({ kind: "rank", pos: "before", other: "A" });
  });

  test("between two cards: after the card above", () => {
    expect(planDrop(cards, "A", "todo", 1, declares)).toEqual({ kind: "rank", pos: "after", other: "B" });
  });

  test("where it already is: nothing", () => {
    expect(planDrop(cards, "B", "todo", 1, declares)).toEqual({ kind: "none" });
  });

  test("a neighbour in another space is no place to rank against", () => {
    const mixed = [card("A", "todo", "a"), card("Z", "todo", "b", "other"), card("B", "todo", "c")];
    expect(planDrop(mixed, "B", "todo", 1, declares)).toEqual({ kind: "rank", pos: "after", other: "A" });
    const among = [card("A", "todo", "a"), card("Z", "todo", "b", "other"), card("Y", "todo", "c", "other")];
    expect(planDrop(among, "A", "todo", 1, declares)).toEqual({ kind: "refused", reason: "other_container" });
  });

  test("among unranked cards: the bottom", () => {
    const fresh = [card("A", "todo", ""), card("B", "todo", "")];
    expect(planDrop(fresh, "B", "todo", 0, declares)).toEqual({ kind: "rank", pos: "bottom" });
  });
});

describe("planDrop across columns", () => {
  test("a declared move, placed after the card above", () => {
    expect(planDrop(cards, "B", "in_progress", 1, declares)).toEqual({
      kind: "transition",
      to: "in_progress",
      then: { pos: "after", other: "D" },
    });
  });

  test("a declared move to the top of a column: before its first card", () => {
    expect(planDrop(cards, "B", "in_progress", 0, declares)).toEqual({
      kind: "transition",
      to: "in_progress",
      then: { pos: "before", other: "D" },
    });
  });

  test("into an empty column: the move alone", () => {
    expect(planDrop(cards, "D", "in_review", 0, declares)).toEqual({ kind: "transition", to: "in_review" });
  });

  test("a move the Type does not declare is refused before anything is written", () => {
    expect(planDrop(cards, "A", "done", 0, declares)).toEqual({ kind: "refused", reason: "no_transition" });
  });
});

describe("runDrop", () => {
  function deps(refuse = false) {
    const calls: string[] = [];
    return {
      calls,
      deps: {
        transition: vi.fn(async (c: string, to: string, fields?: Record<string, string>) => {
          calls.push(`transition ${c} ${to}${fields ? ` ${JSON.stringify(fields)}` : ""}`);
          if (refuse) throw new Error("MISSING_FIELDS");
        }),
        rank: vi.fn(async (c: string, pos: string, other?: string) => {
          calls.push(`rank ${c} ${pos}${other ? ` ${other}` : ""}`);
        }),
      },
    };
  }

  test("a move, then the rank", async () => {
    const d = deps();
    await runDrop(d.deps, "D", { kind: "transition", to: "done", then: { pos: "after", other: "E" } }, { f_resolution: "fixed" });
    expect(d.calls).toEqual(['transition D done {"f_resolution":"fixed"}', "rank D after E"]);
  });

  test("a refused move writes no rank, and the refusal is thrown", async () => {
    const d = deps(true);
    await expect(runDrop(d.deps, "D", { kind: "transition", to: "done", then: { pos: "after", other: "E" } })).rejects.toThrow(
      "MISSING_FIELDS",
    );
    expect(d.calls).toEqual(["transition D done"]);
    expect(d.deps.rank).not.toHaveBeenCalled();
  });

  test("a rank move is one rank write", async () => {
    const d = deps();
    await runDrop(d.deps, "C", { kind: "rank", pos: "before", other: "A" });
    expect(d.calls).toEqual(["rank C before A"]);
  });

  test("nothing and refused plans write nothing", async () => {
    const d = deps();
    await runDrop(d.deps, "C", { kind: "none" });
    await runDrop(d.deps, "C", { kind: "refused", reason: "no_transition" });
    expect(d.calls).toEqual([]);
  });
});

describe("boardKey", () => {
  const columns: Record<Column, string[]> = { todo: ["A", "B", "C"], in_progress: ["D"], in_review: [], done: ["E"] };

  test("Space picks the focused card up where it is", () => {
    expect(boardKey(undefined, " ", "B", columns)).toEqual({ holding: { card: "B", column: "todo", index: 1 } });
    expect(boardKey(undefined, "Enter", "D", columns)).toEqual({ holding: { card: "D", column: "in_progress", index: 0 } });
  });

  test("arrows move it while held, within bounds", () => {
    const held = { card: "B", column: "todo" as Column, index: 1 };
    expect(boardKey(held, "ArrowUp", "B", columns).holding).toEqual({ ...held, index: 0 });
    expect(boardKey({ ...held, index: 0 }, "ArrowUp", "B", columns).holding).toEqual({ ...held, index: 0 });
    // Without B, To do has two cards: the last place is index 2.
    expect(boardKey({ ...held, index: 2 }, "ArrowDown", "B", columns).holding).toEqual({ ...held, index: 2 });
    expect(boardKey(held, "ArrowRight", "B", columns).holding).toEqual({ card: "B", column: "in_progress", index: 1 });
    expect(boardKey({ card: "B", column: "in_progress", index: 1 }, "ArrowRight", "B", columns).holding).toEqual({
      card: "B",
      column: "in_review",
      index: 0,
    });
    expect(boardKey(held, "ArrowLeft", "B", columns).holding).toEqual(held);
  });

  test("Space drops it; Escape puts it back", () => {
    const held = { card: "B", column: "in_progress" as Column, index: 1 };
    expect(boardKey(held, " ", "B", columns)).toEqual({ drop: held });
    expect(boardKey(held, "Escape", "B", columns)).toEqual({ cancel: true });
  });

  test("arrows do nothing to a card not held", () => {
    expect(boardKey(undefined, "ArrowDown", "B", columns)).toEqual({});
  });
});
