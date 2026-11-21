// The queue's views: one ListItems of the desk each.
import { StateCategory } from "@gen/kernel/v1/kernel_pb";
import { describe, expect, test } from "vitest";
import { NS, goal, item, itemName, typeName } from "../test/fixtures";
import { countLabel, inView, queueRequest, queueViews } from "./queue";
import { breachedIndex } from "./sla";

const desk = itemName("desk");
const open = [StateCategory.TRIAGE, StateCategory.ACTIVE, StateCategory.WAITING];
const incidents = [`${NS}/types/incident`];

test("the views, in the order the tabs show them", () => {
  expect(queueViews).toEqual([
    { view: "open", label: "All open" },
    { view: "unassigned", label: "Unassigned" },
    { view: "mine", label: "Mine" },
    { view: "waiting", label: "Waiting for customer" },
    { view: "breached", label: "Breached" },
  ]);
});

describe("queueRequest", () => {
  test("all open, soonest resolution first", () => {
    expect(queueRequest(desk, NS, "open", "resolution")).toEqual({
      parent: desk,
      filter: { types: incidents, categories: open },
      orderBy: "f_due_resolution",
      pageSize: 50,
      pageToken: "",
    });
  });

  test("unassigned, by first response, next page", () => {
    expect(queueRequest(desk, NS, "unassigned", "first_response", "tok")).toEqual({
      parent: desk,
      filter: { types: incidents, categories: open, fields: [{ field: "f_assignee", empty: true }] },
      orderBy: "f_due_first_response",
      pageSize: 50,
      pageToken: "tok",
    });
  });

  test("mine is the caller, as the server reads 'me'", () => {
    expect(queueRequest(desk, NS, "mine", "resolution").filter).toEqual({
      types: incidents,
      categories: open,
      fields: [{ field: "f_assignee", eq: { kind: { case: "stringValue", value: "me" } } }],
    });
  });

  test("waiting is one state", () => {
    expect(queueRequest(desk, NS, "waiting", "resolution").filter).toEqual({ types: incidents, states: ["waiting_for_customer"] });
  });

  test("breached reads the open incidents; the goals pick them out", () => {
    expect(queueRequest(desk, NS, "breached", "resolution").filter).toEqual({ types: incidents, categories: open });
  });
});

describe("inView", () => {
  const v2 = item("a", typeName("incident", 2));
  const v1 = item("old", typeName("incident", 1));
  const b = item("b", typeName("incident", 2));
  const idx = breachedIndex([goal("g", "b", "running", { f_metric: "resolution", f_breached_at: "2026-11-21T09:00:00.000000Z" })]);

  test("incident@2 only: older versions are left to the admin UI", () => {
    expect(inView([v2, v1, b], "open", idx).map((i) => i.name)).toEqual([v2.name, b.name]);
  });

  test("breached keeps the incidents with a breached open goal", () => {
    expect(inView([v2, v1, b], "breached", idx).map((i) => i.name)).toEqual([b.name]);
  });
});

test("counts without totals", () => {
  expect(countLabel(12, false)).toBe("12");
  expect(countLabel(50, true)).toBe("50+");
  expect(countLabel(0, false)).toBe("0");
});
