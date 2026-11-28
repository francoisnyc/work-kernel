// Dates and times in words (the Inbox, the look, item 3): an instant as
// the reader says it, in their locale and time zone, with the full instant
// for a tooltip. Pinned here to en-GB, as the brief's examples are, and to
// explicit time zones; the page passes the browser's.
//
//   - dayAndTime: "today 1:43 pm", "tomorrow 9:00 am", "yesterday 4:10 pm"
//     for the days around now, on the reader's calendar; "28 Nov" for
//     another day this year, "28 Nov 2025" for another year;
//   - fromNow: "4 h ago", "in 19 h", the largest unit that fits, rounded
//     down (never more than is left), "now" within a second; wall clock;
//   - fullInstant: the whole instant, date, time to the second, and zone,
//     for a title attribute;
//   - anything that is not an instant is "" for all three.
import { describe, expect, test } from "vitest";
import { dayAndTime, fromNow, fullInstant } from "./when";

const GB = { locale: "en-GB", timeZone: "Europe/London" };
const NY = { locale: "en-GB", timeZone: "America/New_York" };
// Saturday 28 November 2026, 14:00 in London (UTC in winter).
const now = new Date("2026-11-28T14:00:00Z");

describe("dayAndTime", () => {
  test("today, tomorrow, yesterday: the word and the time", () => {
    expect(dayAndTime("2026-11-28T13:43:48.660Z", now, GB)).toBe("today 1:43 pm");
    expect(dayAndTime("2026-11-29T09:00:00Z", now, GB)).toBe("tomorrow 9:00 am");
    expect(dayAndTime("2026-11-27T16:10:00Z", now, GB)).toBe("yesterday 4:10 pm");
  });

  test("another day: the date alone, the year only when it is not this one", () => {
    expect(dayAndTime("2026-11-20T09:00:00Z", now, GB)).toBe("20 Nov");
    expect(dayAndTime("2026-12-03T09:00:00Z", now, GB)).toBe("3 Dec");
    expect(dayAndTime("2025-11-28T09:00:00Z", now, GB)).toBe("28 Nov 2025");
  });

  test("on the reader's calendar: past midnight in London is still today in New York", () => {
    // 01:30 UTC on the 29th is 20:30 on the 28th in New York.
    const late = new Date("2026-11-29T01:30:00Z");
    expect(dayAndTime("2026-11-29T01:00:00Z", late, GB)).toBe("today 1:00 am");
    expect(dayAndTime("2026-11-29T01:00:00Z", late, NY)).toBe("today 8:00 pm");
    expect(dayAndTime("2026-11-29T14:00:00Z", late, NY)).toBe("tomorrow 9:00 am");
  });

  test("the offset of the value does not matter: it is an instant", () => {
    expect(dayAndTime("2026-11-28T08:43:48-05:00", now, GB)).toBe("today 1:43 pm");
  });

  test("not an instant: nothing", () => {
    expect(dayAndTime("", now, GB)).toBe("");
    expect(dayAndTime("2026-11-28", now, GB)).toBe("");
    expect(dayAndTime("soon", now, GB)).toBe("");
  });
});

describe("fromNow", () => {
  test("behind and ahead of now", () => {
    expect(fromNow("2026-11-28T10:00:00Z", now)).toBe("4 h ago");
    expect(fromNow("2026-11-29T09:00:00Z", now)).toBe("in 19 h");
    expect(fromNow("2026-11-28T14:05:00Z", now)).toBe("in 5 min");
    expect(fromNow("2026-11-25T13:00:00Z", now)).toBe("3 d ago");
  });

  test("rounded down: never more than is left", () => {
    expect(fromNow("2026-11-28T15:59:59Z", now)).toBe("in 1 h");
  });

  test("within a second: now", () => {
    expect(fromNow("2026-11-28T14:00:00.400Z", now)).toBe("now");
  });

  test("not an instant: nothing", () => {
    expect(fromNow("tomorrow", now)).toBe("");
  });
});

describe("fullInstant", () => {
  test("the date, the time to the second and the zone, for a tooltip", () => {
    const full = fullInstant("2026-11-28T13:43:48.660Z", GB);
    expect(full).toContain("28 November 2026");
    expect(full).toContain("13:43:48");
    expect(full).toMatch(/GMT|UTC|Greenwich/);
    expect(fullInstant("2026-11-28T13:43:48.660Z", NY)).toContain("08:43:48");
  });

  test("not an instant: nothing", () => {
    expect(fullInstant("never", GB)).toBe("");
  });
});
