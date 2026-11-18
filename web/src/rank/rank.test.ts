// @vitest-environment node
// The rank port against the golden vectors the Go package writes
// (internal/rank/vectors_test.go). Go is the reference: the same inputs
// must give the same answers here, byte for byte.
import { readFileSync } from "node:fs";
import { describe, expect, test } from "vitest";
import { plan, type Card, type Position, type Query, type Source } from "./move";
import {
  ALPHABET,
  MAX_GROWTH,
  MAX_TIED_RUN,
  RankError,
  THRESHOLD,
  between,
  compare,
  evenly,
  needsRebalance,
  valid,
  type Int64N,
} from "./rank";

type Vectors = {
  alphabet: string;
  threshold: number;
  maxGrowth: number;
  maxTiedRun: number;
  valid: { key: string; want: boolean }[];
  compare: { a: string; b: string; want: number }[];
  needsRebalance: { key: string; want: boolean }[];
  evenly: { lo: string; hi: string; n: number; keys: string[]; error: string }[];
  between: { lo: string; hi: string; draw: string; key: string; error: string }[];
  plan: {
    name: string;
    cards: Card[];
    moved: string;
    pos: Position;
    other: Card;
    draw: string;
    writes: Card[];
    error: string;
  }[];
};

const vectors = JSON.parse(
  readFileSync(new URL("../../../internal/rank/testdata/vectors.json", import.meta.url), "utf8"),
) as Vectors;

// The vectors' fixed jitter: asked for an integer in [0, n), answer
// (n-1)*NUM/DEN rounded down.
function fixedDraw(draw: string): Int64N {
  const [num, den] = draw.split("/").map((s) => BigInt(s)) as [bigint, bigint];
  return (n) => ((n - 1n) * num) / den;
}

// Runs f and names its outcome as the vectors do: the value, or the error.
function outcome<T>(f: () => T): { value?: T; error: string } {
  try {
    return { value: f(), error: "" };
  } catch (e) {
    if (e instanceof RankError) return { error: e.code };
    throw e;
  }
}

// A container in memory, read as the kernel's order_by reads one.
function memory(cards: Card[]): Source {
  return {
    async scan(q: Query) {
      const byteOrder = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0);
      return cards
        .filter((c) => c.key !== "")
        .filter((c) => (q.eq ? c.key === q.eq : q.gt ? c.key > q.gt : q.lt ? c.key < q.lt : true))
        .sort((x, y) => (q.desc ? byteOrder(y.key, x.key) : byteOrder(x.key, y.key)) || byteOrder(x.id, y.id))
        .slice(0, q.limit);
    },
  };
}

test("the vectors file has every section", () => {
  for (const section of ["valid", "compare", "needsRebalance", "evenly", "between", "plan"] as const) {
    expect(vectors[section].length, section).toBeGreaterThan(0);
  }
});

test("the constants are Go's", () => {
  expect(ALPHABET).toBe(vectors.alphabet);
  expect(THRESHOLD).toBe(vectors.threshold);
  expect(MAX_GROWTH).toBe(vectors.maxGrowth);
  expect(MAX_TIED_RUN).toBe(vectors.maxTiedRun);
});

describe("valid", () => {
  test.each(vectors.valid)("valid($key) = $want", ({ key, want }) => {
    expect(valid(key)).toBe(want);
  });
});

describe("compare", () => {
  test("orders byte by byte, as every vector says", () => {
    for (const { a, b, want } of vectors.compare) {
      expect(compare(a, b), `compare(${a}, ${b})`).toBe(want);
    }
  });
});

describe("needsRebalance", () => {
  test.each(vectors.needsRebalance)("a key of $key.length bytes: $want", ({ key, want }) => {
    expect(needsRebalance(key)).toBe(want);
  });
});

describe("evenly", () => {
  test.each(vectors.evenly)("evenly($lo, $hi, $n)", ({ lo, hi, n, keys, error }) => {
    const got = outcome(() => evenly(lo, hi, n));
    expect(got.error).toBe(error);
    if (error === "") expect(got.value).toEqual(keys);
  });
});

describe("between", () => {
  test.each(vectors.between)("between($lo, $hi) at draw $draw", ({ lo, hi, draw, key, error }) => {
    const got = outcome(() => between(lo, hi, fixedDraw(draw)));
    expect(got.error).toBe(error);
    if (error === "") expect(got.value).toBe(key);
  });

  // Without a fixed draw the jitter is random: still a key strictly
  // between, and not always the same one.
  test("with the default jitter: valid, strictly between, and it varies", () => {
    const seen = new Set<string>();
    for (let i = 0; i < 200; i++) {
      const k = between("V", "W");
      expect(valid(k)).toBe(true);
      expect(compare("V", k)).toBe(-1);
      expect(compare(k, "W")).toBe(-1);
      expect(k.length).toBeLessThanOrEqual(1 + MAX_GROWTH);
      seen.add(k);
    }
    expect(seen.size).toBeGreaterThan(20);
  });
});

describe("plan", () => {
  test.each(vectors.plan)("$name (draw $draw)", async ({ cards, moved, pos, other, draw, writes, error }) => {
    let got = { writes: [] as Card[], error: "" };
    try {
      got.writes = await plan(memory(cards), moved, pos, other, fixedDraw(draw));
    } catch (e) {
      if (!(e instanceof RankError)) throw e;
      got.error = e.code;
    }
    expect(got.error).toBe(error);
    expect(got.writes).toEqual(writes);
  });

  // The reads a move costs: one at an end, three next to a card (the tied
  // run and a neighbour on each side), as pli's (journal, Jira part 1).
  test("reads the container once for top, three times next to a card", async () => {
    const cards = [
      { id: "a", key: "F" },
      { id: "b", key: "K" },
      { id: "c", key: "P" },
    ];
    const counting = (): Source & { reads: number } => {
      const src = memory(cards);
      const s = { reads: 0, scan: (q: Query) => (s.reads++, src.scan(q)) };
      return s;
    };
    const top = counting();
    await plan(top, "c", "top");
    expect(top.reads).toBe(1);
    const next = counting();
    await plan(next, "c", "before", { id: "b", key: "K" });
    expect(next.reads).toBe(3);
  });
});
