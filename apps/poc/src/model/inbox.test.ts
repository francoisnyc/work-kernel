// "Waiting for you": the requests an approver must decide, oldest first,
// with how long each has waited. The API has no time-in-state: when a
// request entered submitted comes from its history.
import { describe, expect, test } from "vitest";
import { Kind, NS, event, item, typeName } from "../test/fixtures";
import { inboxOrder, inboxRow, inboxSearch, waitedLabel } from "./inbox";

const vr = typeName("vacation_request", 3);
const at = (day: number, h = 9) => new Date(Date.UTC(2026, 10, day, h)).toISOString();
const request = (id: string, created: string, hold = false) =>
  item(id, vr, {
    state: "submitted",
    creator: "user:ana",
    at: created,
    fields: { f_start: "2099-07-01", f_end: "2099-07-05", f_days: "5", f_approver: "user:bob" },
    hold: hold ? { check: "deduct", fromState: "submitted", requestedState: "approved" } : undefined,
  });
const entered = (when: string, seq: number) =>
  event(Kind.TRANSITIONED, when, { actor: "hook:vacation_request:hr_check", onBehalfOf: "user:ana", fromState: "draft", toState: "submitted", seq });

test("the search: submitted requests whose approver is the caller, one large page", () => {
  expect(inboxSearch(NS)).toEqual({
    parent: NS,
    filter: {
      types: [`${NS}/types/vacation_request`],
      states: ["submitted"],
      fields: [{ field: "f_approver", eq: { kind: { case: "stringValue", value: "me" } } }],
    },
    pageSize: 500,
  });
});

describe("inboxRow", () => {
  test("waiting since the last time it entered submitted (after a recall and a resubmit)", () => {
    const events = [
      event(Kind.CREATED, at(1), { actor: "user:ana", toState: "draft", seq: 1 }),
      entered(at(2), 2),
      event(Kind.TRANSITIONED, at(3), { actor: "user:ana", fromState: "submitted", toState: "draft", seq: 3 }),
      entered(at(4), 4),
    ];
    expect(inboxRow(request("r1", at(1)), events)).toMatchObject({ waitingSince: at(4), refused: undefined, checking: false });
  });

  test("without a history (not read yet, or hidden), its create time", () => {
    expect(inboxRow(request("r1", at(1)), []).waitingSince).toBe(at(1));
  });

  test("an approval refused since it entered submitted is said, by its reason", () => {
    const events = [
      entered(at(2), 2),
      event(Kind.TRANSITION_REQUESTED, at(3, 10), { actor: "user:bob", requestedState: "approved", seq: 3 }),
      event(Kind.TRANSITION_DENIED, at(3, 11), {
        actor: "reaction:vacation_request:deduct",
        onBehalfOf: "user:bob",
        requestedState: "approved",
        reason: "insufficient_balance",
        seq: 4,
      }),
    ];
    expect(inboxRow(request("r1", at(1)), events).refused).toBe("insufficient_balance");
  });

  test("a refusal before the last entry is forgotten", () => {
    const events = [
      entered(at(2), 2),
      event(Kind.TRANSITION_DENIED, at(3), { requestedState: "approved", reason: "insufficient_balance", seq: 3 }),
      event(Kind.TRANSITIONED, at(4), { actor: "user:ana", fromState: "submitted", toState: "draft", seq: 4 }),
      entered(at(5), 5),
    ];
    expect(inboxRow(request("r1", at(1)), events).refused).toBeUndefined();
  });

  test("an open hold is an approval being checked", () => {
    expect(inboxRow(request("r1", at(1), true), []).checking).toBe(true);
  });
});

describe("inboxOrder", () => {
  test("oldest first; ties by name", () => {
    const rows = [
      inboxRow(request("c", at(1)), [entered(at(5), 2)]),
      inboxRow(request("a", at(1)), [entered(at(3), 2)]),
      inboxRow(request("b", at(1)), [entered(at(5), 2)]),
      inboxRow(request("d", at(2)), []),
    ];
    expect(inboxOrder(rows).map((r) => r.item.name.split("/").pop())).toEqual(["d", "a", "b", "c"]);
  });

  test("does not change what it is given", () => {
    const rows = [inboxRow(request("b", at(1)), [entered(at(5), 2)]), inboxRow(request("a", at(1)), [entered(at(3), 2)])];
    inboxOrder(rows);
    expect(rows.map((r) => r.item.name.split("/").pop())).toEqual(["b", "a"]);
  });
});

describe("waitedLabel: how long, on the wall clock", () => {
  const now = new Date("2026-11-21T12:00:00Z");
  test.each([
    ["2026-11-21T11:59:30Z", "less than a minute"],
    ["2026-11-21T11:59:00Z", "1 min"],
    ["2026-11-21T11:15:00Z", "45 min"],
    ["2026-11-21T11:00:00Z", "1 h"],
    ["2026-11-21T08:30:00Z", "3 h"],
    ["2026-11-20T12:00:00Z", "1 day"],
    ["2026-11-18T09:00:00Z", "3 days"],
    ["2026-11-21T12:05:00Z", "less than a minute"],
  ])("since %s: %s", (since, want) => {
    expect(waitedLabel(since, now)).toBe(want);
  });
});
