// How far an instant is from now, for a datetime value on an Item's page
// (Jira part 2, step 3b).
//
// It is WALL-CLOCK time and says so. An instant such as a goal's due
// instant was computed in working time on a calendar the page does not
// know: "in 2 h" on the clock may be twenty working minutes, or two
// working days. The label must not let it read as time left to work.

// An instant with a date, a time and an offset, as the server returns one
// and as a caller may send one.
const instantForm = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;

const units: [number, string][] = [
  [86_400, "d"],
  [3_600, "h"],
  [60, "min"],
  [1, "s"],
];

// relativeTime is "in 2 h (wall clock)" or "3 min ago (wall clock)": the
// largest unit that fits, rounded down, so it never says more than is
// left. Within a second of now it is "now (wall clock)"; "" for anything
// that is not an instant.
export function relativeTime(instant: string, now: Date): string {
  if (!instantForm.test(instant)) return "";
  // Date keeps milliseconds; the microseconds beyond do not change a label.
  const at = Date.parse(instant.replace(/(\.\d{3})\d+/, "$1"));
  if (Number.isNaN(at)) return "";
  const seconds = Math.floor(Math.abs(at - now.getTime()) / 1000);
  if (seconds < 1) return "now (wall clock)";
  const [size, unit] = units.find(([s]) => seconds >= s)!;
  const n = Math.floor(seconds / size);
  return at > now.getTime() ? `in ${n} ${unit} (wall clock)` : `${n} ${unit} ago (wall clock)`;
}
