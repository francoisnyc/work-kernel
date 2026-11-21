// The process map: a Type's states as boxes in category order, its moves
// as arrows, the automatic steps it declares as system steps, and where
// an Item stands on it. Generic over the Type: the vacation request, the
// ticket, the incident and the story are all drawn by the same function.
import { create } from "@bufbuild/protobuf";
import { TransitionSchema, type Type } from "@gen/kernel/v1/kernel_pb";
import { describe, expect, test } from "vitest";
import { Kind, event, incidentType, storyType, ticketType, vacationType, vacationTypeV1 } from "../test/fixtures";
import { durationLabel, layoutProcess, progressOf, whoMay, type MapEdge, type MapNode, type Point, type ProcessMap } from "./process";

const at = (min: number) => new Date(Date.UTC(2026, 10, 21, 9, min)).toISOString();
const node = (m: ProcessMap, state: string): MapNode => {
  const n = m.nodes.find((x) => x.state === state);
  if (!n) throw new Error(`no box for ${state}`);
  return n;
};
const edge = (m: ProcessMap, from: string, to: string): MapEdge => {
  const e = m.edges.find((x) => x.from === from && x.to === to);
  if (!e) throw new Error(`no arrow ${from} -> ${to}`);
  return e;
};
const place = (m: ProcessMap) => Object.fromEntries(m.nodes.map((n) => [n.state, [n.column, n.row]]));

describe("boxes: one column per category, in category order", () => {
  test("vacation_request@3: draft, then waiting, then the end states together (done first)", () => {
    const m = layoutProcess(vacationType);
    expect(place(m)).toEqual({
      draft: [0, 0],
      submitted: [1, 0],
      approved: [2, 0],
      rejected: [2, 1],
      withdrawn: [2, 2],
      expired: [2, 3],
    });
    expect(m.nodes.map((n) => n.label)).toEqual(["Draft", "Waiting for approval", "Approved", "Rejected", "Withdrawn", "Expired"]);
  });

  test("ticket@6: categories in their order, states in declared order inside a column", () => {
    expect(place(layoutProcess(ticketType))).toEqual({
      triage: [0, 0],
      open: [1, 0],
      in_progress: [2, 0],
      approved: [2, 1],
      waiting: [3, 0],
      waiting_for_customer: [3, 1],
      resolved: [4, 0],
      canceled: [4, 1],
    });
  });

  test("incident@2 and story@3", () => {
    expect(place(layoutProcess(incidentType))).toEqual({
      new: [0, 0],
      in_progress: [1, 0],
      waiting_for_customer: [2, 0],
      resolved: [3, 0],
      closed: [3, 1],
      canceled: [3, 2],
    });
    expect(place(layoutProcess(storyType))).toEqual({
      todo: [0, 0],
      in_progress: [1, 0],
      in_review: [1, 1],
      done: [2, 0],
      canceled: [2, 1],
    });
  });

  test("columns go left to right, rows top to bottom", () => {
    const m = layoutProcess(ticketType);
    for (const a of m.nodes)
      for (const b of m.nodes) {
        if (a.column < b.column) expect(a.x + a.width).toBeLessThan(b.x);
        if (a.column === b.column && a.row < b.row) expect(a.y + a.height).toBeLessThan(b.y);
      }
  });
});

describe("arrows: every declared move, with its name and who may take it", () => {
  test("vacation_request@3, in declared order", () => {
    const m = layoutProcess(vacationType);
    expect(m.edges.map((e) => ({ from: e.from, to: e.to, label: e.label, who: e.who, route: e.route, system: e.system }))).toEqual([
      { from: "draft", to: "submitted", label: "Submit", who: "Requester", route: "next", system: false },
      { from: "submitted", to: "approved", label: "Approve", who: "Approver (not the requester)", route: "next", system: false },
      { from: "submitted", to: "rejected", label: "Reject", who: "Approver (not the requester)", route: "next", system: false },
      { from: "submitted", to: "draft", label: "Recall", who: "Requester", route: "back", system: false },
      { from: "submitted", to: "withdrawn", label: "Withdraw", who: "Requester", route: "next", system: false },
      { from: "draft", to: "withdrawn", label: "Withdraw", who: "Requester", route: "skip", system: false },
      { from: "submitted", to: "expired", label: "Expire", who: "System", route: "next", system: true },
      { from: "approved", to: "withdrawn", label: "Withdraw", who: "Requester", route: "same", system: false },
    ]);
  });

  test("routes on other Types: back, skip and within a column", () => {
    const t = layoutProcess(ticketType);
    expect(edge(t, "in_progress", "approved").route).toBe("same");
    expect(edge(t, "in_progress", "open").route).toBe("back");
    expect(edge(t, "triage", "canceled").route).toBe("skip");
    expect(edge(t, "approved", "resolved").route).toBe("skip");
    expect(edge(t, "waiting", "in_progress").route).toBe("back");
    const s = layoutProcess(storyType);
    expect(edge(s, "in_review", "in_progress").route).toBe("same");
    expect(edge(s, "in_review", "done").route).toBe("next");
    expect(edge(s, "done", "todo").route).toBe("back");
    expect(edge(s, "todo", "canceled").route).toBe("skip");
    const i = layoutProcess(incidentType);
    expect(edge(i, "resolved", "closed")).toMatchObject({ route: "same", who: "System", system: true });
    expect(edge(i, "new", "canceled")).toMatchObject({ route: "skip", who: "Requester or Desk agent" });
    expect(edge(i, "in_progress", "canceled")).toMatchObject({ route: "skip", who: "Anyone" });
  });

  test("a move with no word of its own is 'Move to' its state", () => {
    expect(edge(layoutProcess(ticketType), "triage", "open").label).toBe("Move to Open");
  });
});

describe("who may take a move, read from allowed and except", () => {
  const t = (allowed: string[], except: string[] = []) => create(TransitionSchema, { fromState: "a", toState: "b", allowed, except });
  test.each([
    [[], [], "Anyone"],
    [["creator"], [], "Requester"],
    [["field:f_approver"], ["creator"], "Approver (not the requester)"],
    [["field:f_assignee"], [], "Assignee"],
    [["role:desk_agent"], [], "Desk agent"],
    [["creator", "role:desk_agent"], [], "Requester or Desk agent"],
    [["principal:system:timer"], [], "System"],
    [["principal:user:dana"], [], "dana"],
  ])("%j except %j: %s", (allowed, except, want) => {
    expect(whoMay(t(allowed, except))).toBe(want);
  });
});

describe("system steps: what the Type declares that the kernel does by itself", () => {
  test("vacation_request@3: the check, the balance link, the reminders, the expiry, the deduction and its refund", () => {
    const m = layoutProcess(vacationType);
    expect(edge(m, "draft", "submitted").steps).toEqual([{ kind: "check", text: "Checked by the system before it applies" }]);
    expect(node(m, "submitted").steps).toEqual([
      { kind: "link", text: "Links the requester's leave balance" },
      { kind: "reminder", text: "Reminder to the approver after 2 days, then every day, 2 at most" },
    ]);
    expect(edge(m, "submitted", "expired").steps).toEqual([{ kind: "timer", text: "After 5 days" }]);
    expect(edge(m, "submitted", "approved").steps).toEqual([
      { kind: "reaction", text: "Adds Days to Used on the balance, before the move applies" },
    ]);
    expect(edge(m, "approved", "withdrawn").steps).toEqual([{ kind: "reaction", text: "Gives back what Deduct took" }]);
    for (const e of m.edges.filter((x) => !["draft>submitted", "submitted>expired", "submitted>approved", "approved>withdrawn"].includes(`${x.from}>${x.to}`)))
      expect(e.steps, `${e.from} -> ${e.to}`).toEqual([]);
    for (const n of m.nodes.filter((x) => x.state !== "submitted")) expect(n.steps, n.state).toEqual([]);
  });

  test("vacation_request@1: only the check", () => {
    const m = layoutProcess(vacationTypeV1);
    expect(m.nodes.map((n) => n.state)).not.toContain("expired");
    expect(m.edges.flatMap((e) => e.steps)).toEqual([{ kind: "check", text: "Checked by the system before it applies" }]);
    expect(m.nodes.flatMap((n) => n.steps)).toEqual([]);
  });

  test("ticket@6: timers on moves people can also make, a move and a create reaction", () => {
    const m = layoutProcess(ticketType);
    expect(edge(m, "in_progress", "open")).toMatchObject({ system: false, steps: [{ kind: "timer", text: "After 1 day" }] });
    expect(edge(m, "waiting", "in_progress").steps).toEqual([{ kind: "timer", text: "After 3 days" }]);
    expect(edge(m, "in_progress", "approved")).toMatchObject({ who: "Approver", steps: [{ kind: "check", text: "Checked by the system before it applies" }] });
    expect(edge(m, "waiting_for_customer", "in_progress")).toMatchObject({
      who: "Support",
      steps: [{ kind: "reaction", text: "Moves here when a comment is added inside" }],
    });
    expect(edge(m, "open", "in_progress").steps).toEqual([{ kind: "reaction", text: "Creates a checklist" }]);
  });
});

describe("durations as the Type declares them", () => {
  test.each([
    ["120h", "5 days"],
    ["48h", "2 days"],
    ["24h", "1 day"],
    ["36h", "1 day 12 h"],
    ["2h", "2 h"],
    ["90m", "1 h 30 min"],
    ["45s", "45 s"],
  ])("%s is %s", (d, want) => {
    expect(durationLabel(d)).toBe(want);
  });
});

describe("where an Item stands", () => {
  const history = [
    event(Kind.CREATED, at(0), { actor: "user:ana", toState: "draft", seq: 1 }),
    event(Kind.TRANSITION_REQUESTED, at(1), { actor: "user:ana", requestedState: "submitted", seq: 2 }),
    event(Kind.TRANSITIONED, at(2), { actor: "hook:vacation_request:hr_check", onBehalfOf: "user:ana", fromState: "draft", toState: "submitted", seq: 3 }),
    event(Kind.TRANSITIONED, at(3), { actor: "user:ana", fromState: "submitted", toState: "draft", seq: 4 }),
    event(Kind.TRANSITIONED, at(4), { actor: "hook:vacation_request:hr_check", onBehalfOf: "user:ana", fromState: "draft", toState: "submitted", seq: 5 }),
    event(Kind.TRANSITION_DENIED, at(5), { actor: "reaction:vacation_request:deduct", onBehalfOf: "user:bob", requestedState: "approved", reason: "insufficient_balance", seq: 6 }),
    event(Kind.TRANSITIONED, at(6), { actor: "reaction:vacation_request:deduct", onBehalfOf: "user:bob", fromState: "submitted", toState: "approved", seq: 7 }),
  ];

  test("progressOf: the current state, the states it has been in, the moves made (a refusal is no move)", () => {
    expect(progressOf(vacationType, "approved", history)).toEqual({
      current: "approved",
      visited: ["draft", "submitted", "approved"],
      taken: ["draft>submitted", "submitted>draft", "submitted>approved"],
    });
  });

  test("a new Item is in its initial state, even when its created event names none", () => {
    expect(progressOf(vacationType, "draft", [event(Kind.CREATED, at(0), { actor: "user:ana" })])).toEqual({
      current: "draft",
      visited: ["draft"],
      taken: [],
    });
  });

  test("the map: the current step, the steps done ticked, the moves made drawn as taken", () => {
    const m = layoutProcess(vacationType, progressOf(vacationType, "approved", history));
    expect(Object.fromEntries(m.nodes.map((n) => [n.state, n.status]))).toEqual({
      draft: "done",
      submitted: "done",
      approved: "current",
      rejected: "ahead",
      withdrawn: "ahead",
      expired: "ahead",
    });
    expect(m.edges.filter((e) => e.taken).map((e) => `${e.from}>${e.to}`)).toEqual(["draft>submitted", "submitted>approved", "submitted>draft"]);
  });

  test("without progress, nothing is current or done", () => {
    expect(new Set(layoutProcess(vacationType).nodes.map((n) => n.status))).toEqual(new Set(["ahead"]));
    expect(layoutProcess(vacationType).edges.some((e) => e.taken)).toBe(false);
  });
});

// The drawing itself, checked the same way on every Type: arrows are
// right-angled lines that start and end on their boxes' edges, never cross
// a box, never run along another arrow (unless the two share an end), and
// everything fits in the map.
describe.each([
  ["vacation_request@3", vacationType],
  ["vacation_request@1", vacationTypeV1],
  ["ticket@6", ticketType],
  ["incident@2", incidentType],
  ["story@3", storyType],
] as [string, Type][])("geometry of %s", (_name, type) => {
  const m = layoutProcess(type);
  const box = (s: string) => node(m, s);
  const segments = (e: MapEdge) => e.points.slice(1).map((p, i) => [e.points[i]!, p] as [Point, Point]);
  const onBorder = (p: Point, n: MapNode) =>
    (Math.abs(p.x - n.x) < 0.5 || Math.abs(p.x - n.x - n.width) < 0.5) && p.y >= n.y - 0.5 && p.y <= n.y + n.height + 0.5
      ? true
      : (Math.abs(p.y - n.y) < 0.5 || Math.abs(p.y - n.y - n.height) < 0.5) && p.x >= n.x - 0.5 && p.x <= n.x + n.width + 0.5;
  const crossesInside = ([a, b]: [Point, Point], n: MapNode) => {
    const [x1, x2] = [Math.min(a.x, b.x), Math.max(a.x, b.x)];
    const [y1, y2] = [Math.min(a.y, b.y), Math.max(a.y, b.y)];
    return x2 > n.x + 0.5 && x1 < n.x + n.width - 0.5 && y2 > n.y + 0.5 && y1 < n.y + n.height - 0.5;
  };
  const overlap = ([a, b]: [Point, Point], [c, d]: [Point, Point]) => {
    if (a.y === b.y && c.y === d.y && a.y === c.y)
      return Math.min(Math.max(a.x, b.x), Math.max(c.x, d.x)) - Math.max(Math.min(a.x, b.x), Math.min(c.x, d.x)) > 1;
    if (a.x === b.x && c.x === d.x && a.x === c.x)
      return Math.min(Math.max(a.y, b.y), Math.max(c.y, d.y)) - Math.max(Math.min(a.y, b.y), Math.min(c.y, d.y)) > 1;
    return false;
  };

  test("one arrow per declared move, one box per state", () => {
    expect(m.edges).toHaveLength(type.transitions.length);
    expect(m.nodes).toHaveLength(type.states.length);
  });

  test("arrows are right-angled and run from their box's edge to the target's edge", () => {
    for (const e of m.edges) {
      expect(e.points.length, `${e.from} -> ${e.to}`).toBeGreaterThanOrEqual(2);
      for (const [a, b] of segments(e)) expect(a.x === b.x || a.y === b.y, `${e.from} -> ${e.to}`).toBe(true);
      expect(onBorder(e.points[0]!, box(e.from)), `${e.from} -> ${e.to} starts on ${e.from}`).toBe(true);
      expect(onBorder(e.points.at(-1)!, box(e.to)), `${e.from} -> ${e.to} ends on ${e.to}`).toBe(true);
    }
  });

  test("no arrow crosses a box", () => {
    for (const e of m.edges)
      for (const s of segments(e))
        for (const n of m.nodes) expect(crossesInside(s, n), `${e.from} -> ${e.to} crosses ${n.state}`).toBe(false);
  });

  test("two arrows that share no end never run along each other", () => {
    for (const e of m.edges)
      for (const f of m.edges) {
        if (e === f || e.from === f.from || e.to === f.to) continue;
        for (const s of segments(e))
          for (const t of segments(f)) expect(overlap(s, t), `${e.from} -> ${e.to} runs along ${f.from} -> ${f.to}`).toBe(false);
      }
  });

  test("labels are placed apart", () => {
    for (const e of m.edges)
      for (const f of m.edges) {
        if (e === f) continue;
        const d = Math.hypot(e.labelAt.x - f.labelAt.x, e.labelAt.y - f.labelAt.y);
        expect(d, `labels of ${e.from} -> ${e.to} and ${f.from} -> ${f.to}`).toBeGreaterThanOrEqual(8);
      }
  });

  test("everything fits in the map", () => {
    for (const n of m.nodes) {
      expect(n.x).toBeGreaterThanOrEqual(0);
      expect(n.y).toBeGreaterThanOrEqual(0);
      expect(n.x + n.width).toBeLessThanOrEqual(m.width);
      expect(n.y + n.height).toBeLessThanOrEqual(m.height);
    }
    for (const p of m.edges.flatMap((e) => [...e.points, e.labelAt])) {
      expect(p.x).toBeGreaterThanOrEqual(0);
      expect(p.y).toBeGreaterThanOrEqual(0);
      expect(p.x).toBeLessThanOrEqual(m.width);
      expect(p.y).toBeLessThanOrEqual(m.height);
    }
  });
});
