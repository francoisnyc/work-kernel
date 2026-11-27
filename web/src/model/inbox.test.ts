// @vitest-environment node
// The Inbox page's model (track 1 item 5, step 4, PR 2).
//
// The rule these tests hold: the kernel decides who may act. Rows come
// from `waiting_on`, counts from CountItems, buttons from `Item.moves`.
// The browser reads no `allowed`, `except` or claim to decide a button.
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { describe, expect, test } from "vitest";
import { FieldType, StateCategory, TypeSchema } from "../gen/kernel/v1/kernel_pb";
import { NS, allTypes, aSubmittedVacation, item, itemName, ticket, typeName, vacation } from "../test/fixtures";
import {
  INBOX_PAGE,
  askedBy,
  countLabel,
  inboxCount,
  inboxSearch,
  inboxTabs,
  rowMoves,
  waitingFor,
  whyYours,
  type InboxTab,
} from "./inbox";

// A purchase: finance takes it from a queue (a claim), decides, or gives
// it back. Shaped like the bake-offs' Type.
const purchase = create(TypeSchema, {
  name: typeName("purchase", 1),
  typeId: "purchase",
  version: 1,
  fields: [
    { id: "f_title", type: FieldType.STRING },
    { id: "f_manager", type: FieldType.PRINCIPAL },
    { id: "f_claimed_by", type: FieldType.PRINCIPAL },
  ],
  states: [
    { id: "finance_queue", category: StateCategory.WAITING },
    { id: "finance_review", category: StateCategory.ACTIVE },
    { id: "approved", category: StateCategory.DONE },
    { id: "rejected", category: StateCategory.CANCELED },
  ],
  transitions: [
    { fromState: "finance_queue", toState: "finance_review", allowed: ["role:finance"] },
    { fromState: "finance_review", toState: "approved", allowed: ["role:finance"], except: ["creator"] },
    { fromState: "finance_review", toState: "rejected", allowed: ["role:finance"] },
    { fromState: "finance_review", toState: "finance_queue", allowed: ["role:finance"], clears: ["f_claimed_by"] },
    { fromState: "approved", toState: "finance_review", allowed: ["role:cfo"] },
  ],
});

const me: InboxTab = { key: "me", label: "Waiting on me", waitingOn: "me" };
const finance: InboxTab = { key: "role:finance", label: "Finance", waitingOn: "role:finance", role: "finance" };

describe("inboxTabs", () => {
  test("Waiting on me first, then one tab per role the caller holds that some Type's `allowed` names, in byte order", () => {
    expect(inboxTabs(["auditor", "finance", "cfo", "approver"], [...allTypes, purchase])).toEqual([
      me,
      { key: "role:approver", label: "Approver", waitingOn: "role:approver", role: "approver" },
      { key: "role:cfo", label: "Cfo", waitingOn: "role:cfo", role: "cfo" },
      finance,
    ]);
  });

  test("a role no Type names gets no tab; a role the caller does not hold gets none either", () => {
    expect(inboxTabs(["hr_admin"], [purchase])).toEqual([me]);
    expect(inboxTabs([], [purchase, ticket])).toEqual([me]);
  });

  test("no roles and no Types: Waiting on me alone", () => {
    expect(inboxTabs([], [])).toEqual([me]);
  });
});

describe("inboxSearch", () => {
  test("Waiting on me, oldest first: waiting_on me, order_by waiting_since, a page of 50, nothing else", () => {
    expect(inboxSearch({ parent: NS, tab: me, order: "oldest", place: "" })).toEqual({
      parent: NS,
      pageSize: INBOX_PAGE,
      orderBy: "waiting_since",
      filter: { waitingOn: "me" },
    });
    expect(INBOX_PAGE).toBe(50);
  });

  test("a role's tab asks for that role's queue", () => {
    expect(inboxSearch({ parent: NS, tab: finance, order: "oldest", place: "" }).filter).toEqual({ waitingOn: "role:finance" });
  });

  test("newest first is waiting_since desc", () => {
    expect(inboxSearch({ parent: NS, tab: me, order: "newest", place: "" }).orderBy).toBe("waiting_since desc");
  });

  test("a place is filter.in; the next page sends the token; a write's commit_seq is min_seq", () => {
    expect(inboxSearch({ parent: NS, tab: finance, order: "newest", place: itemName(7) }, { pageToken: "t1", minSeq: 42n })).toEqual({
      parent: NS,
      pageSize: 50,
      orderBy: "waiting_since desc",
      filter: { waitingOn: "role:finance", in: itemName(7) },
      pageToken: "t1",
      minSeq: 42n,
    });
  });

  test("it never names a Type, a state or a field: the kernel works out what waits", () => {
    const f = inboxSearch({ parent: NS, tab: me, order: "oldest", place: itemName(7) }).filter!;
    expect(Object.keys(f).sort()).toEqual(["in", "waitingOn"]);
  });
});

describe("inboxCount", () => {
  test("the tab's filter, with the place, and no page or order", () => {
    expect(inboxCount({ parent: NS, tab: me, place: "" })).toEqual({ parent: NS, filter: { waitingOn: "me" } });
    expect(inboxCount({ parent: NS, tab: finance, place: itemName(7) })).toEqual({
      parent: NS,
      filter: { waitingOn: "role:finance", in: itemName(7) },
    });
  });
});

describe("countLabel", () => {
  test("a count as it is, 50+ when capped, nothing before the answer", () => {
    expect(countLabel({ count: 12, capped: false })).toBe("12");
    expect(countLabel({ count: 0, capped: false })).toBe("0");
    expect(countLabel({ count: 50, capped: false })).toBe("50");
    expect(countLabel({ count: 50, capped: true })).toBe("50+");
    expect(countLabel(undefined)).toBe("");
  });
});

describe("rowMoves", () => {
  const inReview = (moves: string[]) =>
    item({ name: itemName(20), type: purchase.name, state: "finance_review", fields: { f_claimed_by: "user:fay" }, moves });

  test("exactly the server's moves, in the server's order", () => {
    expect(rowMoves(purchase, inReview(["approved", "rejected", "finance_queue"])).map((t) => t.toState)).toEqual([
      "approved",
      "rejected",
      "finance_queue",
    ]);
    expect(rowMoves(purchase, inReview(["finance_queue", "approved"])).map((t) => t.toState)).toEqual(["finance_queue", "approved"]);
  });

  test("each is the Type's own transition from the Item's state, so its form and rules show", () => {
    const [back] = rowMoves(purchase, inReview(["finance_queue"]));
    expect(back?.fromState).toBe("finance_review");
    expect(back?.clears).toEqual(["f_claimed_by"]);
  });

  test("no moves from the server: no button, whatever the Type allows", () => {
    expect(rowMoves(purchase, inReview([]))).toEqual([]);
    // vacation's approver moves name field:f_approver, which holds bob:
    // the browser does not work that out.
    expect(rowMoves(vacation, aSubmittedVacation)).toEqual([]);
  });

  test("a move the server lists is offered even where the Type's `allowed` would not suggest it", () => {
    // approved -> finance_review names role:cfo only; the server said the
    // caller may make it, and the server is who decides.
    const approved = item({ name: itemName(21), type: purchase.name, state: "approved", moves: ["finance_review"] });
    expect(rowMoves(purchase, approved).map((t) => t.toState)).toEqual(["finance_review"]);
  });

  test("a move the page's copy of the Type does not have is still a button", () => {
    const moved = rowMoves(purchase, inReview(["escalated"]));
    expect(moved.map((t) => [t.fromState, t.toState])).toEqual([["finance_review", "escalated"]]);
    expect(rowMoves(undefined, inReview(["approved"])).map((t) => [t.fromState, t.toState])).toEqual([["finance_review", "approved"]]);
  });
});

describe("whyYours", () => {
  const queued = item({ name: itemName(30), type: purchase.name, state: "finance_queue", fields: { f_manager: "user:bob" } });
  const claimed = item({
    name: itemName(31),
    type: purchase.name,
    state: "finance_review",
    fields: { f_manager: "user:bob", f_claimed_by: "user:fay", f_title: "user:fay" },
  });

  // Since the Inbox for people (PR 2b): the tab's own name says it, so
  // the row adds nothing.
  test("on a role's tab: the tab says it", () => {
    expect(whyYours(finance, purchase, queued, "user:fay")).toBe("");
    expect(whyYours(finance, purchase, claimed, "user:fay")).toBe("");
  });

  // Since the Inbox for people (PR 2b): "you are its {label}", the ID
  // where the field has no label (none here).
  test("on Waiting on me: the principal fields that hold the caller, by their IDs, in the Type's order", () => {
    expect(whyYours(me, purchase, claimed, "user:fay")).toBe("you are its f_claimed_by");
    expect(whyYours(me, purchase, claimed, "user:bob")).toBe("you are its f_manager");
    const both = item({ name: itemName(32), type: purchase.name, state: "finance_review", fields: { f_manager: "user:fay", f_claimed_by: "user:fay" } });
    expect(whyYours(me, purchase, both, "user:fay")).toBe("you are its f_manager, f_claimed_by");
    expect(whyYours(me, vacation, aSubmittedVacation, "user:bob")).toBe("you are its f_approver");
  });

  test("a string field that happens to hold the caller's ID is not a reason", () => {
    expect(whyYours(me, purchase, claimed, "user:fay")).not.toContain("f_title");
  });

  test("on Waiting on me with no field that holds the caller: nothing is claimed about why", () => {
    // fay holds finance and the purchase is in the queue: the kernel put
    // it in her view, and the browser does not re-derive the reason.
    expect(whyYours(me, purchase, queued, "user:fay")).toBe("");
    expect(whyYours(me, undefined, claimed, "user:fay")).toBe("");
  });
});

// Sent back to the requester (track 1 item 5, step 5): a state whose Type
// says waits_on_requester (GetType), and the caller is the Item's
// requester. Two comparisons of what the data says; no rule is evaluated.
describe("whyYours: sent back to you", () => {
  const helpdesk = create(TypeSchema, {
    name: typeName("case", 1),
    typeId: "case",
    version: 1,
    fields: [{ id: "f_title", type: FieldType.STRING }, { id: "f_agent", type: FieldType.PRINCIPAL }],
    states: [
      { id: "open", category: StateCategory.WAITING },
      { id: "asked", category: StateCategory.WAITING, waitsOnRequester: true },
    ],
  });
  const asked = (init: { creator: string; creatorOnBehalfOf?: string; fields?: Record<string, string> }) =>
    item({ name: itemName(50), type: helpdesk.name, state: "asked", ...init });

  test("the requester, in a state that waits on its requester: sent back to you", () => {
    expect(whyYours(me, helpdesk, asked({ creator: "customer:kim" }), "customer:kim")).toBe("sent back to you");
    expect(whyYours(me, helpdesk, asked({ creator: "user:ana" }), "user:ana")).toBe("sent back to you");
    // An agent created it for kim: kim asked for it.
    expect(whyYours(me, helpdesk, asked({ creator: "agent:portal-bot", creatorOnBehalfOf: "customer:kim" }), "customer:kim")).toBe("sent back to you");
  });

  test("it says that before the fields that hold the caller", () => {
    expect(whyYours(me, helpdesk, asked({ creator: "user:ana", fields: { f_agent: "user:ana" } }), "user:ana")).toBe("sent back to you");
  });

  test("not for someone else, not in another state, not on a role's tab", () => {
    expect(whyYours(me, helpdesk, asked({ creator: "customer:kim" }), "customer:lee")).toBe("");
    expect(whyYours(me, helpdesk, asked({ creator: "agent:portal-bot", creatorOnBehalfOf: "customer:kim" }), "agent:portal-bot")).toBe("");
    const open = item({ name: itemName(51), type: helpdesk.name, state: "open", creator: "user:ana" });
    expect(whyYours(me, helpdesk, open, "user:ana")).toBe("");
    expect(whyYours(finance, helpdesk, asked({ creator: "user:fay" }), "user:fay")).toBe("");
    expect(whyYours(me, undefined, asked({ creator: "user:ana" }), "user:ana")).toBe("");
  });
});

describe("askedBy", () => {
  test("the creator; the user an agent created it for, when there is one", () => {
    expect(askedBy(item({ name: itemName(40), creator: "user:ana" }))).toBe("user:ana");
    expect(askedBy(item({ name: itemName(41), creator: "agent:prep-bot", creatorOnBehalfOf: "user:ana" }))).toBe("user:ana");
    expect(askedBy(item({ name: itemName(42), creator: "customer:kim" }))).toBe("customer:kim");
  });
});

describe("waitingFor", () => {
  const now = new Date("2026-11-26T12:00:00Z");
  const ago = (seconds: number) => timestampFromDate(new Date(now.getTime() - seconds * 1000));

  test("the largest unit that fits, rounded down", () => {
    expect(waitingFor(ago(3 * 86_400 + 5 * 3600), now)).toBe("waiting 3 d");
    expect(waitingFor(ago(5 * 3600 + 59 * 60), now)).toBe("waiting 5 h");
    expect(waitingFor(ago(20 * 60 + 1), now)).toBe("waiting 20 min");
    expect(waitingFor(ago(59), now)).toBe("waiting 59 s");
  });

  test("under a second, or a clock a little behind the server's: just arrived", () => {
    expect(waitingFor(ago(0), now)).toBe("just arrived");
    expect(waitingFor(ago(-3), now)).toBe("just arrived");
  });

  test("no state time: nothing", () => {
    expect(waitingFor(undefined, now)).toBe("");
  });
});
