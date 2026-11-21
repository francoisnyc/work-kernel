// The moves offered as buttons: read from the Type, never evaluated.
import { describe, expect, test } from "vitest";
import { epicType, incidentType, storyType } from "../test/fixtures";
import { declaresMove, movesFrom } from "./moves";

describe("movesFrom", () => {
  test("staff get every declared move from the state, in order, with its label and required fields", () => {
    expect(movesFrom(incidentType, "in_progress", "staff")).toEqual([
      { to: "waiting_for_customer", label: "Wait for customer", required: [] },
      { to: "resolved", label: "Resolve", required: ["f_resolution"] },
      { to: "canceled", label: "Cancel", required: [] },
    ]);
  });

  test("a move only the system makes is not a button", () => {
    expect(movesFrom(incidentType, "resolved", "staff")).toEqual([{ to: "in_progress", label: "Reopen", required: [] }]);
  });

  test("a customer gets only the moves whose allowed names creator", () => {
    expect(movesFrom(incidentType, "new", "customer")).toEqual([{ to: "canceled", label: "Cancel request", required: [] }]);
    expect(movesFrom(incidentType, "in_progress", "customer")).toEqual([]);
    expect(movesFrom(incidentType, "resolved", "customer")).toEqual([{ to: "in_progress", label: "Reopen request", required: [] }]);
  });

  test("a guarded move is offered all the same: the server decides", () => {
    expect(movesFrom(epicType, "in_progress", "staff")).toEqual([{ to: "done", label: "Mark done", required: [] }]);
  });
});

describe("declaresMove", () => {
  test("reads the Type's transitions", () => {
    expect(declaresMove(storyType, "todo", "in_progress")).toBe(true);
    expect(declaresMove(storyType, "in_progress", "done")).toBe(true);
    expect(declaresMove(storyType, "todo", "done")).toBe(false);
    expect(declaresMove(storyType, "todo", "todo")).toBe(false);
  });

  test("not a move only the system makes", () => {
    expect(declaresMove(incidentType, "resolved", "closed")).toBe(false);
  });
});
