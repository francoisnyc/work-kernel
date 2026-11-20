// How far an instant is from now, for a datetime value on an Item's page
// (Jira part 2, step 3b). It is WALL-CLOCK time and says so: a due instant
// computed in working time on a calendar is "in 2 h" on the clock even
// when only twenty working minutes are left. Nothing here knows a Type, a
// field or a calendar: only an instant and now.
import { describe, expect, test } from "vitest";
import { relativeTime } from "./reltime";

const now = new Date("2026-11-23T12:00:00Z");
const at = (iso: string) => relativeTime(iso, now);

describe("relativeTime", () => {
  test("ahead of now: in N unit, labelled as wall clock", () => {
    expect(at("2026-11-23T14:00:00.000000Z")).toBe("in 2 h (wall clock)");
    expect(at("2026-11-23T12:20:00.000000Z")).toBe("in 20 min (wall clock)");
    expect(at("2026-11-23T12:00:45.000000Z")).toBe("in 45 s (wall clock)");
    expect(at("2026-11-26T12:00:00.000000Z")).toBe("in 3 d (wall clock)");
  });

  test("behind now: N unit ago", () => {
    expect(at("2026-11-23T11:57:00.000000Z")).toBe("3 min ago (wall clock)");
    expect(at("2026-11-23T07:00:00.000000Z")).toBe("5 h ago (wall clock)");
    expect(at("2026-11-21T12:00:00.000000Z")).toBe("2 d ago (wall clock)");
    expect(at("2026-11-23T11:59:50.000000Z")).toBe("10 s ago (wall clock)");
  });

  test("the largest unit that fits, rounded down: never more than is left", () => {
    expect(at("2026-11-23T13:59:59.000000Z")).toBe("in 1 h (wall clock)");
    expect(at("2026-11-23T12:59:59.000000Z")).toBe("in 59 min (wall clock)");
    expect(at("2026-11-24T11:59:59.000000Z")).toBe("in 23 h (wall clock)");
    expect(at("2026-11-23T10:00:01.000000Z")).toBe("1 h ago (wall clock)");
  });

  test("within a second of now", () => {
    expect(at("2026-11-23T12:00:00.000000Z")).toBe("now (wall clock)");
    expect(at("2026-11-23T12:00:00.400000Z")).toBe("now (wall clock)");
    expect(at("2026-11-23T11:59:59.600000Z")).toBe("now (wall clock)");
  });

  test("the offset of the value does not matter: it is an instant", () => {
    expect(at("2026-11-23T15:00:00+01:00")).toBe("in 2 h (wall clock)");
  });

  test("anything that is not an instant says nothing", () => {
    expect(at("")).toBe("");
    expect(at("soon")).toBe("");
    expect(at("2026-11-23")).toBe("");
  });
});
