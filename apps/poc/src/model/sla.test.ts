// Service levels: what the goals record, shown as it is. Breached comes
// from f_breached_at, met or missed from f_outcome; the only thing the app
// adds is Overdue, between the due instant and the recorded breach.
import { StateCategory } from "@gen/kernel/v1/kernel_pb";
import { describe, expect, test } from "vitest";
import { Kind, NS, event, goal, item, itemName, typeName } from "../test/fixtures";
import { breachSearch, breachedIndex, dueCell, goalStatus, goalTimeline, metricLabel } from "./sla";

const now = new Date("2026-11-21T12:00:00Z");
const later = "2026-11-21T14:00:00.000000Z";
const earlier = "2026-11-21T11:00:00.000000Z";

test("metric labels", () => {
  expect(metricLabel("first_response")).toBe("First response");
  expect(metricLabel("resolution")).toBe("Resolution");
});

describe("goalStatus", () => {
  test("running before its due instant", () => {
    expect(goalStatus(goal("g", "i", "running", { f_due_at: later }), now)).toEqual({ kind: "running", due: later });
  });

  test("running past its due instant, no breach recorded yet: overdue", () => {
    expect(goalStatus(goal("g", "i", "running", { f_due_at: earlier }), now)).toEqual({ kind: "overdue", due: earlier });
  });

  test("a recorded breach is a breach, whatever the clock says", () => {
    const g = goal("g", "i", "running", { f_due_at: later, f_breached_at: earlier });
    expect(goalStatus(g, now)).toEqual({ kind: "breached", breachedAt: earlier, paused: false });
  });

  test("paused, with and without a breach", () => {
    expect(goalStatus(goal("g", "i", "paused", {}), now)).toEqual({ kind: "paused" });
    expect(goalStatus(goal("g", "i", "paused", { f_breached_at: earlier }), now)).toEqual({
      kind: "breached",
      breachedAt: earlier,
      paused: true,
    });
  });

  test("completed: the goal's own outcome, never worked out here", () => {
    // A met goal whose due instant the clock has passed is still met.
    expect(goalStatus(goal("g", "i", "completed", { f_outcome: "met", f_due_at: earlier }), now)).toEqual({ kind: "met" });
    // A missed goal with no breach mark is still missed.
    expect(goalStatus(goal("g", "i", "completed", { f_outcome: "missed" }), now)).toEqual({ kind: "missed" });
    expect(goalStatus(goal("g", "i", "completed", {}), now)).toEqual({ kind: "completed" });
  });

  test("canceled", () => {
    expect(goalStatus(goal("g", "i", "canceled", { f_breached_at: earlier }), now)).toEqual({ kind: "canceled" });
  });
});

describe("the queue's breach state", () => {
  test("one search: open goals with a recorded breach", () => {
    expect(breachSearch(NS)).toEqual({
      parent: NS,
      filter: {
        types: [`${NS}/types/sla_goal`],
        states: ["running", "paused"],
        fields: [{ field: "f_breached_at", notEmpty: true }],
      },
      pageSize: 500,
    });
  });

  test("an index of the incidents those goals are in, by metric", () => {
    const idx = breachedIndex([
      goal("g1", "inc-a", "running", { f_metric: "first_response", f_breached_at: earlier }),
      goal("g2", "inc-b", "paused", { f_metric: "resolution", f_breached_at: earlier }),
      // Not breached: the search would not return it, the index ignores it.
      goal("g3", "inc-c", "running", { f_metric: "resolution" }),
    ]);
    expect(idx.has(itemName("inc-a"), "first_response")).toBe(true);
    expect(idx.has(itemName("inc-a"), "resolution")).toBe(false);
    expect(idx.has(itemName("inc-b"), "resolution")).toBe(true);
    expect(idx.has(itemName("inc-c"), "resolution")).toBe(false);
    expect(idx.incidents().sort()).toEqual([itemName("inc-a"), itemName("inc-b")]);
  });

  const incident = (state: string, fields: Record<string, string>) =>
    item("inc-a", typeName("incident", 2), { state, fields, category: StateCategory.ACTIVE });

  test("a countdown from the due instant the incident shows", () => {
    const idx = breachedIndex([]);
    expect(dueCell(incident("in_progress", { f_due_resolution: later }), "resolution", idx, now)).toEqual({
      kind: "countdown",
      due: later,
    });
  });

  test("overdue between the due instant and the recorded breach", () => {
    const idx = breachedIndex([]);
    expect(dueCell(incident("in_progress", { f_due_first_response: earlier }), "first_response", idx, now)).toEqual({
      kind: "overdue",
      due: earlier,
    });
  });

  test("breached once a goal records it, even if the incident still shows a due instant", () => {
    const idx = breachedIndex([goal("g1", "inc-a", "running", { f_metric: "first_response", f_breached_at: earlier })]);
    expect(dueCell(incident("in_progress", { f_due_first_response: earlier }), "first_response", idx, now)).toEqual({
      kind: "breached",
    });
  });

  test("paused while waiting for the customer: the resolution goal shows no due instant", () => {
    const idx = breachedIndex([]);
    expect(dueCell(incident("waiting_for_customer", {}), "resolution", idx, now)).toEqual({ kind: "paused" });
  });

  test("nothing running: no due instant", () => {
    const idx = breachedIndex([]);
    expect(dueCell(incident("in_progress", {}), "first_response", idx, now)).toEqual({ kind: "none" });
  });
});

describe("goalTimeline", () => {
  test("a goal's history as its life: started, paused, resumed, breached, met", () => {
    const events = [
      event(Kind.CREATED, "2026-11-21T09:00:00Z", { actor: "reaction:incident:sla_start_resolution", toState: "running", fields: { f_metric: "resolution", f_due_at: "2026-11-21T13:00:00.000000Z" } }),
      event(Kind.TRANSITIONED, "2026-11-21T10:00:00Z", { fromState: "running", toState: "paused", fields: { f_due_at: null, f_elapsed_s: "3600" } }),
      event(Kind.REACTION_APPLIED, "2026-11-21T10:00:01Z", { reaction: "sla_goal:hide_resolution" }),
      event(Kind.TRANSITIONED, "2026-11-21T11:00:00Z", { fromState: "paused", toState: "running", fields: { f_due_at: "2026-11-21T14:00:00.000000Z" } }),
      event(Kind.UPDATED, "2026-11-21T14:00:02Z", { actor: "system:timer", fields: { f_breached_at: "2026-11-21T14:00:00.000000Z" } }),
      event(Kind.HOOK_RAN, "2026-11-21T14:00:03Z", { hook: "notify_breach" }),
      event(Kind.TRANSITIONED, "2026-11-21T15:00:00Z", { fromState: "running", toState: "completed", fields: { f_outcome: "missed" } }),
    ];
    expect(goalTimeline(events)).toEqual([
      { at: "2026-11-21T09:00:00.000Z", kind: "started", due: "2026-11-21T13:00:00.000000Z" },
      { at: "2026-11-21T10:00:00.000Z", kind: "paused" },
      { at: "2026-11-21T11:00:00.000Z", kind: "resumed", due: "2026-11-21T14:00:00.000000Z" },
      // The breach's own instant, not the moment the timer's write landed.
      { at: "2026-11-21T14:00:00.000000Z", kind: "breached" },
      { at: "2026-11-21T15:00:00.000Z", kind: "missed" },
    ]);
  });

  test("met and canceled are what the goal recorded", () => {
    expect(
      goalTimeline([event(Kind.TRANSITIONED, "2026-11-21T09:30:00Z", { fromState: "running", toState: "completed", fields: { f_outcome: "met" } })]),
    ).toEqual([{ at: "2026-11-21T09:30:00.000Z", kind: "met" }]);
    expect(
      goalTimeline([event(Kind.TRANSITIONED, "2026-11-21T09:30:00Z", { fromState: "paused", toState: "canceled" })]),
    ).toEqual([{ at: "2026-11-21T09:30:00.000Z", kind: "canceled" }]);
  });

  test("a pause that unsets the breach mark is a pause, not a breach", () => {
    expect(
      goalTimeline([
        event(Kind.TRANSITIONED, "2026-11-21T09:30:00Z", { fromState: "running", toState: "paused", fields: { f_breached_at: null, f_due_at: null } }),
      ]),
    ).toEqual([{ at: "2026-11-21T09:30:00.000Z", kind: "paused" }]);
  });
});
