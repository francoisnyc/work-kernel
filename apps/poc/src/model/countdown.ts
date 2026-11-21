// How far an instant is from now, for due instants and dates.
//
// It is WALL-CLOCK time. A goal's due instant was computed in working time
// on a calendar the app does not know: "in 2 h" on the clock may be twenty
// working minutes, or two working days. Tables say so in their column
// header ("(wall clock)"); detail pages say it on each value (wallClock).

// An instant with a date, a time and an offset, as the server returns one.
const instantForm = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;

// Date keeps milliseconds; the microseconds beyond change no label.
export function parseInstant(instant: string): number {
  if (!instantForm.test(instant)) return NaN;
  return Date.parse(instant.replace(/(\.\d{3})\d+/, "$1"));
}

const units: [number, string][] = [
  [86_400, "d"],
  [3_600, "h"],
  [60, "min"],
  [1, "s"],
];

// "2 h 15 min": the largest unit that fits and the next one, rounded down.
function span(seconds: number): string {
  const i = units.findIndex(([size]) => seconds >= size);
  const [size, unit] = units[i]!;
  const n = Math.floor(seconds / size);
  const out = `${n} ${unit}`;
  const next = units[i + 1];
  if (!next || unit === "min" || unit === "s") return out;
  const m = Math.floor((seconds - n * size) / next[0]);
  return m > 0 ? `${out} ${m} ${next[1]}` : out;
}

// "in 2 h 15 min", "in 45 min", "in 30 s", "3 min ago", "1 d 4 h ago",
// "now" within a second. At most two units, each rounded down, so it
// never says more than is left. "" for anything that is not an instant.
export function countdown(instant: string, now: Date): string {
  const at = parseInstant(instant);
  if (Number.isNaN(at)) return "";
  const seconds = Math.floor(Math.abs(at - now.getTime()) / 1000);
  if (seconds < 1) return "now";
  return at > now.getTime() ? `in ${span(seconds)}` : `${span(seconds)} ago`;
}

// countdown with the caveat: "in 2 h (wall clock)"; "" for no instant.
export function wallClock(instant: string, now: Date): string {
  const c = countdown(instant, now);
  return c === "" ? "" : `${c} (wall clock)`;
}

// "Updated just now" (under 5 s), "Updated 20 s ago", "Updated 3 min ago";
// "" before the first load.
export function updatedLabel(at: Date | undefined, now: Date): string {
  if (!at) return "";
  const seconds = Math.floor((now.getTime() - at.getTime()) / 1000);
  if (seconds < 5) return "Updated just now";
  if (seconds < 60) return `Updated ${seconds} s ago`;
  return `Updated ${Math.floor(seconds / 60)} min ago`;
}

// An instant for a history line, in UTC: "2026-12-04 17:00 UTC".
export function shortInstant(instant: string): string {
  const at = parseInstant(instant);
  if (Number.isNaN(at)) return instant;
  const iso = new Date(at).toISOString();
  return `${iso.slice(0, 10)} ${iso.slice(11, 16)} UTC`;
}
