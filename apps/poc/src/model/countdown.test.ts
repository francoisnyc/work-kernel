// Due instants on the wall clock.
import { describe, expect, test } from "vitest";
import { countdown, shortInstant, updatedLabel, wallClock } from "./countdown";

const now = new Date("2026-11-21T12:00:00Z");

describe("countdown", () => {
  test.each([
    ["2026-11-21T14:15:59.000000Z", "in 2 h 15 min"],
    ["2026-11-21T14:00:00.000000Z", "in 2 h"],
    ["2026-11-21T12:45:30.000000Z", "in 45 min"],
    ["2026-11-21T12:00:30.000000Z", "in 30 s"],
    ["2026-11-23T16:30:00.000000Z", "in 2 d 4 h"],
    ["2026-11-21T11:57:00.000000Z", "3 min ago"],
    ["2026-11-20T08:00:00.000000Z", "1 d 4 h ago"],
    ["2026-11-21T12:00:00.400000Z", "now"],
    ["2026-11-21T13:00:00+01:00", "now"],
  ])("%s is %s", (instant, text) => {
    expect(countdown(instant, now)).toBe(text);
  });

  test("never says more than is left: units are rounded down", () => {
    expect(countdown("2026-11-21T12:59:59.999999Z", now)).toBe("in 59 min");
  });

  test.each(["", "2026-11-21", "soon", "2026-11-21T12:00:00"])("%j is no instant", (text) => {
    expect(countdown(text, now)).toBe("");
  });
});

test("wallClock adds the caveat", () => {
  expect(wallClock("2026-11-21T14:00:00.000000Z", now)).toBe("in 2 h (wall clock)");
  expect(wallClock("", now)).toBe("");
});

test.each([
  [undefined, ""],
  [new Date("2026-11-21T11:59:57Z"), "Updated just now"],
  [new Date("2026-11-21T11:59:40Z"), "Updated 20 s ago"],
  [new Date("2026-11-21T11:57:00Z"), "Updated 3 min ago"],
])("updatedLabel(%s) is %j", (at, text) => {
  expect(updatedLabel(at, now)).toBe(text);
});

test("a history line's instant is short and in UTC", () => {
  expect(shortInstant("2026-12-04T17:00:00.000000Z")).toBe("2026-12-04 17:00 UTC");
  expect(shortInstant("2026-12-04T18:30:00+01:00")).toBe("2026-12-04 17:30 UTC");
});
