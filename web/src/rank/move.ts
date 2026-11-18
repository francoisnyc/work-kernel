// A move: a port of internal/rank/move.go's Plan (RewriteOrder is not
// ported: the UI does no rebalance).
//
// Phase B: signatures only.
import type { Int64N } from "./rank";

export type Card = { id: string; key: string };
export type Position = "before" | "after" | "top" | "bottom";

// The cards of one container that have a key: all, or those whose key is
// eq, above gt or below lt; in key order, ties by ID ascending either way.
export type Query = { eq?: string; gt?: string; lt?: string; desc?: boolean; limit: number };

export interface Source {
  scan(q: Query): Promise<Card[]>;
}

export type Write = { id: string; key: string };

// What a move writes: one key, or the re-keyed tied run. Throws RankError
// no_rank, tied_run_too_long or changed. other is ignored for top and bottom.
export async function plan(
  _src: Source,
  _moved: string,
  _pos: Position,
  _other?: Card,
  _int64N?: Int64N,
): Promise<Write[]> {
  throw new Error("not implemented");
}
