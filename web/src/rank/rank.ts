// Rank keys: a port of internal/rank/rank.go, held to it by the golden
// vectors in internal/rank/testdata/vectors.json (rank.test.ts).
//
// Phase B: signatures only.

export const ALPHABET = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
export const THRESHOLD = 32;
export const MAX_GROWTH = 4;
export const MAX_TIED_RUN = 8;

// The refusals of this package, by the names the vectors use.
export type RankErrorCode = "no_room" | "invalid_key" | "no_rank" | "tied_run_too_long" | "changed";

export class RankError extends Error {
  constructor(readonly code: RankErrorCode) {
    super(`rank: ${code}`);
  }
}

// Draws the jitter: a uniform integer in [0, n). Tests pass a fixed one.
export type Int64N = (n: bigint) => bigint;

export function valid(_key: string): boolean {
  throw new Error("not implemented");
}

// Byte order, as Postgres under COLLATE "C": never localeCompare.
export function compare(_a: string, _b: string): -1 | 0 | 1 {
  throw new Error("not implemented");
}

export function needsRebalance(_key: string): boolean {
  throw new Error("not implemented");
}

// A key strictly between lo and hi ("" is open). Throws RankError no_room
// or invalid_key.
export function between(_lo: string, _hi: string, _int64N?: Int64N): string {
  throw new Error("not implemented");
}

// n keys strictly between lo and hi, ascending and evenly spaced.
export function evenly(_lo: string, _hi: string, _n: number): string[] {
  throw new Error("not implemented");
}
