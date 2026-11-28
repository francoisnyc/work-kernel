// Dates and times in words (the Inbox, the look, item 3): an instant as
// the reader says it, in their locale and time zone ("today 1:43 pm",
// "tomorrow 9:00 am", "28 Nov"), how far it is from now ("4 h ago",
// "in 19 h"), and the whole instant for a tooltip. Wall-clock time: an
// instant the kernel computed in working time is still shown on the clock.
// The page passes nothing and gets the browser's locale and zone.

export type WhenOpts = { locale?: string; timeZone?: string };

// An instant with a date, a time and an offset, as the server returns one.
const instantForm = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;

// The instant's milliseconds, or undefined for anything that is not one.
function parse(instant: string): number | undefined {
  if (!instantForm.test(instant)) return undefined;
  // Date keeps milliseconds; the microseconds beyond change no word.
  const at = Date.parse(instant.replace(/(\.\d{3})\d+/, "$1"));
  return Number.isNaN(at) ? undefined : at;
}

// The calendar day of ms in the zone, as a whole number of days, so two
// days can be compared and subtracted.
function dayOf(ms: number, opts: WhenOpts): { day: number; year: number } {
  const parts = new Intl.DateTimeFormat("en-CA", { timeZone: opts.timeZone, year: "numeric", month: "2-digit", day: "2-digit" }).formatToParts(ms);
  const get = (t: string) => Number(parts.find((p) => p.type === t)?.value);
  const year = get("year");
  return { day: Date.UTC(year, get("month") - 1, get("day")) / 86_400_000, year };
}

const dayWords: Record<number, string> = { [-1]: "yesterday", 0: "today", 1: "tomorrow" };

// "today 1:43 pm", "tomorrow 9:00 am", "yesterday 4:10 pm" for the days
// around now on the reader's calendar; "28 Nov" for another day this
// year, "28 Nov 2025" for another year; "" for anything not an instant.
export function dayAndTime(instant: string, now: Date, opts: WhenOpts = {}): string {
  const at = parse(instant);
  if (at === undefined) return "";
  const then = dayOf(at, opts);
  const today = dayOf(now.getTime(), opts);
  const word = dayWords[then.day - today.day];
  if (word !== undefined) {
    const time = new Intl.DateTimeFormat(opts.locale, { timeZone: opts.timeZone, hour: "numeric", minute: "2-digit", hour12: true }).format(at);
    // Some locales put a narrow no-break space before "pm".
    return `${word} ${time.replace(/[  ]/g, " ")}`;
  }
  return new Intl.DateTimeFormat(opts.locale, {
    timeZone: opts.timeZone,
    day: "numeric",
    month: "short",
    ...(then.year === today.year ? {} : { year: "numeric" }),
  }).format(at);
}

const units: [number, string][] = [
  [86_400, "d"],
  [3_600, "h"],
  [60, "min"],
  [1, "s"],
];

// "4 h ago", "in 19 h": the largest unit that fits, rounded down, so it
// never says more than is left; "now" within a second; "" for anything
// not an instant.
export function fromNow(instant: string, now: Date): string {
  const at = parse(instant);
  if (at === undefined) return "";
  const seconds = Math.floor(Math.abs(at - now.getTime()) / 1000);
  if (seconds < 1) return "now";
  const [size, unit] = units.find(([s]) => seconds >= s)!;
  const n = Math.floor(seconds / size);
  return at > now.getTime() ? `in ${n} ${unit}` : `${n} ${unit} ago`;
}

// The whole instant, for a title attribute: the date, the time to the
// second and the zone; "" for anything not an instant.
export function fullInstant(instant: string, opts: WhenOpts = {}): string {
  const at = parse(instant);
  if (at === undefined) return "";
  return new Intl.DateTimeFormat(opts.locale, {
    timeZone: opts.timeZone,
    weekday: "long",
    day: "numeric",
    month: "long",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
    timeZoneName: "short",
  }).format(at);
}
