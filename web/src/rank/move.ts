// A move: a port of internal/rank/move.go's Plan (RewriteOrder is not
// ported: the UI does no rebalance). Held to Go by the plan vectors.
import { MAX_TIED_RUN, RankError, between, evenly, type Int64N } from "./rank";

export type Card = { id: string; key: string };
export type Position = "before" | "after" | "top" | "bottom";

// The cards of one container that have a key: all, or those whose key is
// eq, above gt or below lt; in key order, ties by ID ascending either way.
export type Query = { eq?: string; gt?: string; lt?: string; desc?: boolean; limit: number };

export interface Source {
  scan(q: Query): Promise<Card[]>;
}

export type Write = { id: string; key: string };

// Runs f; a no_room from it means a card moved between the plan's reads.
function orChanged<T>(f: () => T): T {
  try {
    return f();
  } catch (e) {
    if (e instanceof RankError && e.code === "no_room") throw new RankError("changed");
    throw e;
  }
}

// What a move writes: one key for the moved card, strictly between its new
// neighbours in the container's order; or, when those neighbours share a
// key, fresh keys for the cards that share it (at most MAX_TIED_RUN).
// Throws RankError no_rank, tied_run_too_long or changed. other is ignored
// for top and bottom.
export async function plan(
  src: Source,
  moved: string,
  pos: Position,
  other?: Card,
  int64N?: Int64N,
): Promise<Write[]> {
  // The key of the first card of q that is not the moved card itself.
  const neighbour = async (q: Omit<Query, "limit">): Promise<string> => {
    const cards = await src.scan({ ...q, limit: 2 });
    return cards.find((c) => c.id !== moved && c.key !== "")?.key ?? "";
  };
  const one = (lo: string, hi: string): Write[] => [{ id: moved, key: orChanged(() => between(lo, hi, int64N)) }];

  if (pos === "top") return one("", await neighbour({}));
  if (pos === "bottom") return one(await neighbour({ desc: true }), "");
  if (!other || other.key === "") throw new RankError("no_rank");

  // The cards that share the other card's key: one more than the cap (and
  // the moved card) tells a run at the cap from a longer one.
  const limit = MAX_TIED_RUN + 2;
  const read = await src.scan({ eq: other.key, limit });
  const complete = read.length < limit;
  const run = read.filter((c) => c.id !== moved);
  let at = run.findIndex((c) => c.id === other.id);

  const below = await neighbour({ lt: other.key, desc: true });
  const above = await neighbour({ gt: other.key });
  // The other card is not among the cards with its key: it was moved.
  if (at < 0 && complete) throw new RankError("changed");
  // The usual case: the other card is at the edge of its run.
  if (pos === "before" && at === 0) return one(below, other.key);
  if (pos === "after" && complete && at === run.length - 1) return one(other.key, above);
  // Tied neighbours: re-key the run, the moved card in its place.
  if (!complete || run.length > MAX_TIED_RUN) throw new RankError("tied_run_too_long");
  if (pos === "after") at++;
  const order = [...run.slice(0, at), { id: moved, key: "" }, ...run.slice(at)];
  const keys = orChanged(() => evenly(below, above, order.length));
  return order.map((c, i) => ({ id: c.id, key: keys[i]! }));
}
