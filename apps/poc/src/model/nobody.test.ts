// Jira 2.4b: allowed: [nobody] is a move no person makes (only a reaction,
// by consent). The apps treat it as they treat a move only the timer
// makes: never offered, never a drop target, drawn as the system's.
import { create } from "@bufbuild/protobuf";
import { TransitionSchema, TypeSchema, StateCategory } from "@gen/kernel/v1/kernel_pb";
import { expect, test } from "vitest";
import { item, typeName } from "../test/fixtures";
import { declaresMove, isSystemOnly, movesFor, movesFrom } from "./moves";
import { layoutProcess, whoMay } from "./process";

const tr = (fromState: string, toState: string, allowed: string[]) => create(TransitionSchema, { fromState, toState, allowed });
const goalType = create(TypeSchema, {
  name: typeName("sla_goal", 2),
  typeId: "sla_goal",
  version: 2,
  initialState: "running",
  states: [
    { id: "running", category: StateCategory.ACTIVE },
    { id: "completed", category: StateCategory.DONE },
    { id: "canceled", category: StateCategory.CANCELED },
  ],
  transitions: [tr("running", "completed", ["nobody"]), tr("running", "canceled", ["nobody"])],
});

test("a move nobody makes is the system's, like the timer's", () => {
  expect(isSystemOnly(tr("a", "b", ["nobody"]))).toBe(true);
  expect(isSystemOnly(tr("a", "b", ["principal:system:timer"]))).toBe(true);
  expect(isSystemOnly(tr("a", "b", []))).toBe(false);
  expect(isSystemOnly(tr("a", "b", ["role:sla_admin"]))).toBe(false);
});

test("never offered, never a drop target", () => {
  expect(movesFrom(goalType, "running", "staff")).toEqual([]);
  expect(declaresMove(goalType, "running", "completed")).toBe(false);
  const goal = item("g1", goalType.name, { state: "running" });
  expect(movesFor(goalType, goal, { principal: "user:lea", roles: ["sla_admin"] })).toEqual([]);
});

test("on the map: who is the System, and the arrow is the system's", () => {
  expect(whoMay(tr("a", "b", ["nobody"]))).toBe("System");
  const m = layoutProcess(goalType);
  expect(m.edges.map((e) => [e.who, e.system])).toEqual([
    ["System", true],
    ["System", true],
  ]);
});
