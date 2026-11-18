// Rank keys: a port of internal/rank/rank.go, held to it by the golden
// vectors in internal/rank/testdata/vectors.json (rank.test.ts). Change
// the Go package first, rewrite the vectors, then follow here.
//
// A key is a fraction between 0 and 1 written in base 62, with the digits
// in ASCII order, so comparing keys as bytes compares the fractions. A key
// never ends with the lowest digit. The arithmetic is on BigInt, as Go's
// is on big.Int.

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

const BASE = 62n;
// How many keys between() wants to choose from.
const ROOM = 1000n;
// At an end of the list: a step of 1/END_NEAR to 1/END_FAR of the room left.
const END_NEAR = 32n;
const END_FAR = 8n;
// The least distance evenly() leaves between two keys, in last digits.
const SPACING = BASE;

export function valid(key: string): boolean {
  if (key === "" || key.endsWith(ALPHABET[0]!)) return false;
  for (const ch of key) {
    if (!ALPHABET.includes(ch)) return false;
  }
  return true;
}

// Byte order, as Postgres under COLLATE "C": never localeCompare. (JS
// compares strings by UTF-16 code unit, which is byte order for ASCII.)
export function compare(a: string, b: string): -1 | 0 | 1 {
  return a < b ? -1 : a > b ? 1 : 0;
}

export function needsRebalance(key: string): boolean {
  return key.length > THRESHOLD;
}

// Not a secret: 64 random bits reduced to [0, n) is plenty for a jitter.
const defaultInt64N: Int64N = (n) => {
  const words = crypto.getRandomValues(new Uint32Array(2));
  return ((BigInt(words[0]!) << 32n) | BigInt(words[1]!)) % n;
};

// A key strictly between lo and hi ("" is open). Throws RankError no_room
// or invalid_key.
export function between(lo: string, hi: string, int64N: Int64N = defaultInt64N): string {
  const top = lo === "" && hi !== "";
  const bottom = lo !== "" && hi === "";
  let want = ROOM;
  if (top || bottom) {
    // The step's window is 1/END_FAR - 1/END_NEAR of the gap.
    want = (ROOM * END_NEAR * END_FAR) / (END_NEAR - END_FAR);
  }
  const { n, first, last } = window(lo, hi, want);
  const span = last - first;
  if (top || bottom) {
    const near = span / END_NEAR;
    const far = span / END_FAR;
    const step = near + random(far - near, int64N);
    return format(top ? last - step : first + step, n);
  }
  // The midpoint, give or take a quarter of the gap.
  return format(first + (span >> 2n) + random(span >> 1n, int64N), n);
}

// n keys strictly between lo and hi, ascending and evenly spaced.
export function evenly(lo: string, hi: string, n: number): string[] {
  if (n <= 0) {
    if (lo !== "" && hi !== "" && compare(lo, hi) >= 0) throw new RankError("no_room");
    return [];
  }
  const count = BigInt(n);
  const w = window(lo, hi, (count + 1n) * SPACING);
  const low = w.first - 1n;
  const step = (w.last + 1n - low) / (count + 1n);
  const keys: string[] = [];
  let k = low;
  for (let i = 0; i < n; i++) {
    k += step;
    keys.push(format(k, w.n));
  }
  return keys;
}

// The shortest key length n at which at least `want` keys lie strictly
// between lo and hi, with the first and last of them as integers of n
// base-62 digits.
function window(lo: string, hi: string, want: bigint): { n: number; first: bigint; last: bigint } {
  for (const key of [lo, hi]) {
    if (key !== "" && !valid(key)) throw new RankError("invalid_key");
  }
  if (lo !== "" && hi !== "" && compare(lo, hi) >= 0) throw new RankError("no_room");
  for (let n = 1; ; n++) {
    const first = prefix(lo, n) + 1n;
    let last: bigint;
    if (hi === "") last = BASE ** BigInt(n) - 1n;
    else if (hi.length > n) last = prefix(hi, n);
    else last = prefix(hi, n) - 1n;
    if (last - first + 1n >= want) return { n, first, last };
  }
}

// The first n digits of key as an integer, a shorter key padded with zeros.
function prefix(key: string, n: number): bigint {
  let v = 0n;
  for (let i = 0; i < n; i++) {
    v *= BASE;
    if (i < key.length) v += BigInt(ALPHABET.indexOf(key[i]!));
  }
  return v;
}

// An integer of n base-62 digits as a key, without its trailing zeros.
function format(v: bigint, n: number): string {
  const digits: string[] = new Array(n);
  let rest = v;
  for (let i = n - 1; i >= 0; i--) {
    digits[i] = ALPHABET[Number(rest % BASE)]!;
    rest /= BASE;
  }
  return digits.join("").replace(/0+$/, "");
}

// A uniform integer in [0, max].
function random(max: bigint, int64N: Int64N): bigint {
  if (max <= 0n) return 0n;
  if (max < 1n << 62n) return int64N(max + 1n);
  // A very wide gap: jitter its top 62 bits.
  const shift = BigInt(max.toString(2).length - 62);
  return int64N((max >> shift) + 1n) << shift;
}
