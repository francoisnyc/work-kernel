// How far an instant is from now, for due instants and dates.
//
// It is WALL-CLOCK time. A goal's due instant was computed in working time
// on a calendar the app does not know: "in 2 h" on the clock may be twenty
// working minutes, or two working days. Tables say so in their column
// header ("(wall clock)"); detail pages say it on each value (wallClock).
import { todo } from "./todo";

// "in 2 h 15 min", "in 45 min", "in 30 s", "3 min ago", "1 d 4 h ago",
// "now" within a second. At most two units, each rounded down, so it
// never says more than is left. "" for anything that is not an instant.
export function countdown(_instant: string, _now: Date): string {
  return todo();
}

// countdown with the caveat: "in 2 h (wall clock)"; "" for no instant.
export function wallClock(_instant: string, _now: Date): string {
  return todo();
}

// "Updated just now" (under 5 s), "Updated 20 s ago", "Updated 3 min ago";
// "" before the first load.
export function updatedLabel(_at: Date | undefined, _now: Date): string {
  return todo();
}

// An instant for a history line, in UTC: "2026-12-04 17:00 UTC".
export function shortInstant(_instant: string): string {
  return todo();
}
