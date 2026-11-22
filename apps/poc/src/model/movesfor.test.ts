// The moves a case page offers its viewer: those whose `allowed` names
// them plainly (the requester, the person a field holds, a role they
// hold, or anyone), never a move only the system makes. Guards and
// `except` are never judged here: the server refuses what does not hold.
import { describe, expect, test } from "vitest";
import { balanceType, incidentType, item, typeName, vacationType } from "../test/fixtures";
import { movesFor } from "./moves";

const vr = typeName("vacation_request", 3);
const request = (state: string, creator = "user:ana", onBehalfOf?: string) =>
  item("r1", vr, { state, creator, onBehalfOf, fields: { f_approver: "user:bob" } });
const ana = { principal: "user:ana", roles: [] };
const bob = { principal: "user:bob", roles: ["approver"] };
const dana = { principal: "user:dana", roles: [] };
const moves = (state: string, viewer: { principal: string; roles: string[] }, creator?: string, onBehalfOf?: string) =>
  movesFor(vacationType, request(state, creator, onBehalfOf), viewer).map((m) => m.label);

describe("vacation_request@3", () => {
  test("the requester, on a draft: submit (its required fields named) and withdraw", () => {
    expect(movesFor(vacationType, request("draft"), ana)).toEqual([
      { to: "submitted", label: "Submit", required: ["f_start", "f_end", "f_days", "f_leave_type"] },
      { to: "withdrawn", label: "Withdraw", required: [] },
    ]);
  });

  test("the requester, while it waits: recall and withdraw; never approve, never expire", () => {
    expect(moves("submitted", ana)).toEqual(["Recall", "Withdraw"]);
  });

  test("the approver the field names: approve and reject", () => {
    expect(moves("submitted", bob)).toEqual(["Approve", "Reject"]);
  });

  test("someone else: nothing", () => {
    expect(moves("submitted", dana)).toEqual([]);
    expect(moves("draft", dana)).toEqual([]);
  });

  test("approved: the requester may withdraw (whether the leave has started is the server's to say)", () => {
    expect(moves("approved", ana)).toEqual(["Withdraw"]);
    expect(moves("approved", bob)).toEqual([]);
  });

  test("an end state offers nothing", () => {
    expect(moves("expired", ana)).toEqual([]);
    expect(moves("rejected", bob)).toEqual([]);
  });

  test("a request an agent made for ana is ana's", () => {
    expect(moves("submitted", ana, "agent:triage-bot", "user:ana")).toEqual(["Recall", "Withdraw"]);
    expect(moves("submitted", { principal: "agent:triage-bot", roles: [] }, "agent:triage-bot", "user:ana")).toEqual([]);
  });
});

test("a role the viewer holds", () => {
  const open = item("b1", typeName("leave_balance", 1), { state: "open", fields: { f_owner: "user:ana" } });
  expect(movesFor(balanceType, open, { principal: "user:hana", roles: ["hr_admin"] }).map((m) => m.label)).toEqual(["Close"]);
  expect(movesFor(balanceType, open, ana)).toEqual([]);
});

test("an empty allowed is anyone; a role or the requester, either", () => {
  const incident = item("i1", typeName("incident", 2), { state: "new", creator: "customer:kim" });
  expect(movesFor(incidentType, incident, { principal: "user:sam", roles: ["desk_agent"] }).map((m) => m.to)).toEqual(["in_progress", "canceled"]);
  expect(movesFor(incidentType, incident, { principal: "user:erin", roles: [] }).map((m) => m.to)).toEqual(["in_progress"]);
});
