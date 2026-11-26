import { describe, expect, test } from "vitest";
import {
  NS,
  aSubmittedVacation,
  aTicket,
  allTypes,
  item,
  itemName,
  leaveBalance,
  ticket,
  ticket4,
  vacation,
} from "../test/fixtures";
import {
  displayTitle,
  fieldAccess,
  incomingLinkTypes,
  latestVersions,
  parseTypeName,
  shortId,
  transitionsFrom,
} from "./typeinfo";

test("parseTypeName reads a versioned and a versionless name", () => {
  expect(parseTypeName(`${NS}/types/ticket/versions/5`)).toEqual({ ns: "acme", typeId: "ticket", version: 5 });
  expect(parseTypeName(`${NS}/types/ticket`)).toEqual({ ns: "acme", typeId: "ticket", version: 0 });
});

test("shortId is the first 8 characters of the Item's ID", () => {
  expect(shortId(itemName(42))).toBe("00000042");
});

test("latestVersions keeps the highest version of each Type, by type ID", () => {
  const latest = latestVersions(allTypes);
  expect(latest.map((t) => `${t.typeId}@${t.version}`)).toEqual([
    "bug@1",
    "epic@1",
    "leave_balance@1",
    "sprint@1",
    "story@1",
    "subscription@1",
    "ticket@5",
    "vacation_request@3",
  ]);
  expect(latestVersions([ticket, ticket4])).toEqual([ticket]);
});

describe("fieldAccess", () => {
  test("an ordinary field is editable", () => {
    expect(fieldAccess(ticket, "f_title", "triage")).toEqual({ mode: "editable", roles: [] });
  });

  test("a kernel-written field is never editable, in any state", () => {
    expect(fieldAccess(vacation, "f_approver", "draft").mode).toBe("kernel");
    expect(fieldAccess(vacation, "f_approver", "submitted").mode).toBe("kernel");
    expect(fieldAccess(leaveBalance, "f_used", "open").mode).toBe("kernel");
  });

  test("a field is frozen only in the states that freeze it", () => {
    expect(fieldAccess(vacation, "f_start", "draft").mode).toBe("editable");
    expect(fieldAccess(vacation, "f_start", "submitted").mode).toBe("frozen");
    expect(fieldAccess(vacation, "f_comment", "submitted").mode).toBe("editable");
    expect(fieldAccess(vacation, "f_start", "withdrawn").mode).toBe("editable");
  });

  test("a role-written field stays editable and names its roles", () => {
    expect(fieldAccess(leaveBalance, "f_allowance", "open")).toEqual({ mode: "editable", roles: ["hr_admin"] });
  });

  test("on create there is no state yet: nothing is frozen", () => {
    expect(fieldAccess(vacation, "f_start", "").mode).toBe("editable");
    expect(fieldAccess(vacation, "f_approver", "").mode).toBe("kernel");
  });
});

describe("transitionsFrom", () => {
  test("lists the transitions leaving the state, in the Type's order", () => {
    expect(transitionsFrom(ticket, "in_progress").map((v) => v.transition.toState)).toEqual([
      "open",
      "approved",
      "resolved",
    ]);
    expect(transitionsFrom(ticket, "canceled")).toEqual([]);
  });

  test("offers every transition, whatever its allowed, except and guard say", () => {
    const moves = transitionsFrom(vacation, "submitted");
    expect(moves.map((v) => v.transition.toState)).toEqual(["approved", "rejected", "draft", "withdrawn", "expired"]);
  });

  test("marks a transition only the system can make", () => {
    const moves = transitionsFrom(vacation, "submitted");
    expect(moves.filter((v) => v.systemOnly).map((v) => v.transition.toState)).toEqual(["expired"]);
  });

  test("a role, a field, the creator or nobody named is not system-only", () => {
    for (const v of [...transitionsFrom(ticket, "in_progress"), ...transitionsFrom(leaveBalance, "open")]) {
      expect(v.systemOnly, v.transition.toState).toBe(false);
    }
  });
});

describe("incomingLinkTypes", () => {
  test("a sprint, which declares nothing, is the target of story and bug's sprint link", () => {
    expect(incomingLinkTypes(allTypes, "sprint")).toEqual([
      { name: "sprint_issues", declared: "sprint", from: ["bug", "story"], writable: true },
      { name: "watched_by", declared: "watches", from: ["subscription"], writable: true },
    ]);
  });

  test("a link only the kernel sets is listed, not writable", () => {
    expect(incomingLinkTypes(allTypes, "leave_balance")).toContainEqual({
      name: "requests",
      declared: "balance",
      from: ["vacation_request"],
      writable: false,
    });
  });

  test("a Type's links to itself count: a ticket is blocked_by tickets", () => {
    expect(incomingLinkTypes(allTypes, "ticket").map((l) => l.name)).toEqual(["blocked_by", "duplicates", "watched_by"]);
  });
});

describe("displayTitle", () => {
  test("the first string field that has a value", () => {
    expect(displayTitle(ticket, aTicket)).toBe("printer on fire");
    // f_comment is the vacation request's only string field.
    expect(displayTitle(vacation, aSubmittedVacation)).toBe("skiing");
  });

  test("skips an unset string field for the next one", () => {
    const noTitle = item({ name: itemName(5), type: ticket.name, state: "triage", fields: { f_reporter: "tom" } });
    expect(displayTitle(ticket, noTitle)).toBe("tom");
  });

  test("no string value: the type ID and the short ID", () => {
    const bare = item({ name: itemName(7), type: vacation.name, state: "draft", fields: { f_days: "3" } });
    expect(displayTitle(vacation, bare)).toBe("vacation_request 00000007");
  });
});
