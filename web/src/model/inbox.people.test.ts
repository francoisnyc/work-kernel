// The Inbox for people, PR 2b: how a row is worded, grouped and split
// into its main button and the rest. Pure functions over what the kernel
// sends (the Type's hints, State.claim, Item.moves and Item.withheld);
// none decides who may act.
//
// The owner's decisions (Phase A, 2026-11-27): the main button is the
// move marked primary when the server lists it, else the first it lists;
// labels everywhere, IDs where a Type gives none; Overdue, then Waiting,
// worked out on the rows loaded; a role's ID made readable for its tab;
// names from the dev-token file, else the ID; the withheld wordings.
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { describe, expect, test } from "vitest";
import { FieldType, StateCategory, TypeSchema, WithheldReason, type Type } from "../gen/kernel/v1/kernel_pb";
import { item, itemName, typeName } from "../test/fixtures";
import { personName, type DevUser } from "./devusers";
import {
  askedLine,
  dueOf,
  groupRows,
  holderLine,
  holderOf,
  inboxSearch,
  roleLabel,
  splitMoves,
  stateLabel,
  summaryLine,
  waitedLine,
  whyNot,
  whyYours,
  type InboxTab,
} from "./inbox";

const NOW = new Date("2026-11-27T12:00:00Z");
const at = (iso: string) => iso; // a datetime field's value, as the kernel sends it
const hoursAgo = (h: number) => timestampFromDate(new Date(NOW.getTime() - h * 3_600_000));

// An access request shaped like access_request@2: labels, a summary, a
// due on the state that waits for approval, a primary move.
const access = create(TypeSchema, {
  name: typeName("access", 2),
  typeId: "access",
  version: 2,
  summary: ["f_reason", "f_owner", "f_note"],
  fields: [
    { id: "f_title", type: FieldType.STRING, label: "Request" },
    { id: "f_reason", type: FieldType.STRING, label: "Reason" },
    { id: "f_owner", type: FieldType.PRINCIPAL, label: "Owner" },
    { id: "f_note", type: FieldType.STRING },
    { id: "f_approver", type: FieldType.PRINCIPAL, label: "Approver" },
    { id: "f_due_approval", type: FieldType.DATETIME, label: "Approval due" },
  ],
  states: [
    { id: "pending", category: StateCategory.WAITING, label: "Waiting for approval", due: "f_due_approval" },
    { id: "approved", category: StateCategory.DONE, label: "Approved" },
    { id: "rejected", category: StateCategory.DONE },
    { id: "withdrawn", category: StateCategory.CANCELED },
  ],
  transitions: [
    { fromState: "pending", toState: "rejected", allowed: ["field:f_approver"], label: "Reject" },
    { fromState: "pending", toState: "approved", allowed: ["field:f_approver"], label: "Approve", primary: true },
    { fromState: "pending", toState: "withdrawn", allowed: ["creator"] },
  ],
});

// A desk request shaped like equipment_request@2: a claimed state.
const desk = create(TypeSchema, {
  name: typeName("desk_request", 2),
  typeId: "desk_request",
  version: 2,
  fields: [
    { id: "f_title", type: FieldType.STRING, label: "What is needed" },
    { id: "f_handler", type: FieldType.PRINCIPAL, label: "Handled by" },
  ],
  states: [
    { id: "queue", category: StateCategory.WAITING, label: "In the queue" },
    { id: "in_hand", category: StateCategory.ACTIVE, label: "Being handled", claim: "f_handler" },
    { id: "fulfilled", category: StateCategory.DONE },
  ],
  transitions: [
    { fromState: "queue", toState: "in_hand", allowed: ["role:desk_agent"], label: "Take it", primary: true },
    { fromState: "in_hand", toState: "fulfilled", allowed: ["role:desk_agent"], label: "Mark fulfilled", primary: true },
    { fromState: "in_hand", toState: "queue", allowed: ["role:desk_agent"], label: "Hand back" },
  ],
});

// A Type with no hints at all.
const bare = create(TypeSchema, {
  name: typeName("bare", 1),
  typeId: "bare",
  version: 1,
  fields: [
    { id: "f_title", type: FieldType.STRING },
    { id: "f_owner", type: FieldType.PRINCIPAL },
  ],
  states: [
    { id: "open", category: StateCategory.WAITING },
    { id: "done", category: StateCategory.DONE },
    { id: "gone", category: StateCategory.CANCELED },
  ],
  transitions: [
    { fromState: "open", toState: "done" },
    { fromState: "open", toState: "gone" },
  ],
});

const people: DevUser[] = [
  { name: "ana", token: "a", principal: "user:ana", namespace: "acme", roles: [] },
  { name: "lea", token: "l", principal: "user:lea", namespace: "acme", roles: ["desk_agent"] },
  { name: "sam", token: "s", principal: "user:sam", namespace: "acme", roles: ["desk_agent"] },
];
const nameOf = (p: string) => personName(p, people);

const pendingAccess = (fields: Record<string, string>, moves = ["rejected", "approved"]) =>
  item({ name: itemName(1), type: access.name, state: "pending", creator: "user:ana", fields: { f_title: "Finance share", ...fields }, moves, stateTime: hoursAgo(3) });

describe("personName", () => {
  test("the dev-token file's name, capitalised", () => {
    expect(personName("user:sam", people)).toBe("Sam");
    expect(personName("user:lea", people)).toBe("Lea");
  });
  test("someone the file does not have: their ID", () => {
    expect(personName("user:zoe", people)).toBe("user:zoe");
    expect(personName("agent:triage-bot", people)).toBe("agent:triage-bot");
    expect(personName("user:sam", [])).toBe("user:sam");
  });
});

describe("roleLabel", () => {
  test("a role's ID, readable: underscores are spaces, the first letter a capital", () => {
    expect(roleLabel("desk_agent")).toBe("Desk agent");
    expect(roleLabel("finance")).toBe("Finance");
    expect(roleLabel("sla_admin")).toBe("Sla admin");
  });
});

describe("stateLabel", () => {
  test("the state's label; its ID where the Type gives none, or the Type is unknown", () => {
    expect(stateLabel(access, "pending")).toBe("Waiting for approval");
    expect(stateLabel(access, "rejected")).toBe("rejected");
    expect(stateLabel(undefined, "pending")).toBe("pending");
  });
});

describe("splitMoves: the main button and the rest", () => {
  test("the move marked primary, when the server lists it; the rest in the server's order", () => {
    const { main, others } = splitMoves(access, pendingAccess({}, ["rejected", "approved", "withdrawn"]));
    expect(main?.toState).toBe("approved");
    expect(others.map((t) => t.toState)).toEqual(["rejected", "withdrawn"]);
  });
  test("primary not listed for the caller: the first move the server lists", () => {
    const { main, others } = splitMoves(access, pendingAccess({}, ["withdrawn", "rejected"]));
    expect(main?.toState).toBe("withdrawn");
    expect(others.map((t) => t.toState)).toEqual(["rejected"]);
  });
  test("a Type with no primary: the first listed", () => {
    const it = item({ name: itemName(2), type: bare.name, state: "open", moves: ["gone", "done"] });
    const { main, others } = splitMoves(bare, it);
    expect(main?.toState).toBe("gone");
    expect(others.map((t) => t.toState)).toEqual(["done"]);
  });
  test("no moves: no main button, nothing else", () => {
    const { main, others } = splitMoves(access, pendingAccess({}, []));
    expect(main).toBeUndefined();
    expect(others).toEqual([]);
  });
});

describe("summaryLine", () => {
  test("each summary field with a value, as Label: value, in the summary's order; a person by name", () => {
    expect(summaryLine(access, pendingAccess({ f_reason: "quarterly close", f_owner: "user:lea", f_note: "urgent" }), nameOf)).toEqual([
      "Reason: quarterly close",
      "Owner: Lea",
      "f_note: urgent",
    ]);
  });
  test("an empty field is left out; a Type without a summary says nothing", () => {
    expect(summaryLine(access, pendingAccess({ f_reason: "quarterly close" }), nameOf)).toEqual(["Reason: quarterly close"]);
    expect(summaryLine(bare, item({ name: itemName(2), type: bare.name, state: "open", fields: { f_title: "x" } }), nameOf)).toEqual([]);
  });
});

describe("askedLine", () => {
  test("who asked, by name: the creator, or the user an agent asked for", () => {
    expect(askedLine(pendingAccess({}), "user:sam", nameOf)).toBe("Ana asks");
    const forLea = item({ name: itemName(3), type: access.name, state: "pending", creator: "agent:bot", creatorOnBehalfOf: "user:lea" });
    expect(askedLine(forLea, "user:sam", nameOf)).toBe("Lea asks");
  });
  test("the caller asked: You asked for this one", () => {
    expect(askedLine(pendingAccess({}), "user:ana", nameOf)).toBe("You asked for this one");
  });
  test("someone the dev file does not have: their ID", () => {
    const byZoe = item({ name: itemName(4), type: access.name, state: "pending", creator: "user:zoe" });
    expect(askedLine(byZoe, "user:sam", nameOf)).toBe("user:zoe asks");
  });
});

describe("whyYours: in labels", () => {
  const me: InboxTab = { key: "me", label: "Waiting on me", waitingOn: "me" };
  const team: InboxTab = { key: "role:desk_agent", label: "Desk agent", waitingOn: "role:desk_agent", role: "desk_agent" };
  test("on Waiting on me, the principal fields that hold the caller, by their labels", () => {
    expect(whyYours(me, access, pendingAccess({ f_approver: "user:sam" }), "user:sam")).toBe("you are its Approver");
  });
  test("a field with no label: its ID", () => {
    const it = item({ name: itemName(5), type: bare.name, state: "open", fields: { f_owner: "user:sam" } });
    expect(whyYours(me, bare, it, "user:sam")).toBe("you are its f_owner");
  });
  test("on a team tab, nothing: the tab says it", () => {
    expect(whyYours(team, desk, item({ name: itemName(6), type: desk.name, state: "queue" }), "user:sam")).toBe("");
  });
});

describe("waitedLine", () => {
  test("waiting, since the Item entered its state", () => {
    expect(waitedLine(access, pendingAccess({}), NOW)).toBe("waiting 3 h");
  });
  test("in a state with a claim: in hand, since it was claimed", () => {
    const held = item({ name: itemName(7), type: desk.name, state: "in_hand", fields: { f_handler: "user:lea" }, stateTime: hoursAgo(5) });
    expect(waitedLine(desk, held, NOW)).toBe("in hand 5 h");
  });
});

describe("dueOf", () => {
  test("due in the future: due in, not overdue", () => {
    expect(dueOf(access, pendingAccess({ f_due_approval: at("2026-11-27T14:00:00.000000Z") }), NOW)).toEqual({ text: "due in 2 h", overdue: false });
  });
  test("due in the past: due ago, overdue", () => {
    expect(dueOf(access, pendingAccess({ f_due_approval: at("2026-11-27T08:00:00.000000Z") }), NOW)).toEqual({ text: "due 4 h ago", overdue: true });
  });
  test("no due on the state, no value in the field, or an unknown Type: nothing", () => {
    expect(dueOf(access, pendingAccess({}), NOW)).toBeUndefined();
    expect(dueOf(bare, item({ name: itemName(8), type: bare.name, state: "open" }), NOW)).toBeUndefined();
    expect(dueOf(undefined, pendingAccess({ f_due_approval: "2026-11-27T08:00:00.000000Z" }), NOW)).toBeUndefined();
  });
});

describe("groupRows", () => {
  test("overdue first, then the rest, each in the order given", () => {
    const late1 = pendingAccess({ f_title: "late one", f_due_approval: "2026-11-27T08:00:00.000000Z" });
    const ontime = { ...pendingAccess({ f_title: "on time", f_due_approval: "2026-11-28T08:00:00.000000Z" }), name: itemName(11) };
    const nodue = { ...pendingAccess({ f_title: "no due" }), name: itemName(12) };
    const late2 = { ...pendingAccess({ f_title: "late two", f_due_approval: "2026-11-26T08:00:00.000000Z" }), name: itemName(13) };
    const typeOf = (n: string): Type | undefined => (n === access.name ? access : undefined);
    const g = groupRows([late1, ontime, nodue, late2], typeOf, NOW);
    expect(g.overdue.map((i) => i.fields?.f_title)).toEqual(["late one", "late two"]);
    expect(g.waiting.map((i) => i.fields?.f_title)).toEqual(["on time", "no due"]);
  });
});

describe("the claim's holder", () => {
  const held = (by?: string) =>
    item({ name: itemName(9), type: desk.name, state: "in_hand", fields: by ? { f_handler: by } : {}, stateTime: hoursAgo(1) });
  test("holderOf: the claim field's value (State.claim)", () => {
    expect(holderOf(desk, held("user:lea"))).toBe("user:lea");
    expect(holderOf(desk, held())).toBe("");
    expect(holderOf(desk, item({ name: itemName(10), type: desk.name, state: "queue" }))).toBe("");
  });
  test("holderLine: Lea has it; You have it; Nobody holds it; nothing outside a claimed state", () => {
    expect(holderLine(desk, held("user:lea"), "user:sam", nameOf)).toBe("Lea has it");
    expect(holderLine(desk, held("user:sam"), "user:sam", nameOf)).toBe("You have it");
    expect(holderLine(desk, held(), "user:sam", nameOf)).toBe("Nobody holds it");
    expect(holderLine(desk, item({ name: itemName(10), type: desk.name, state: "queue" }), "user:sam", nameOf)).toBe("");
  });
});

describe("whyNot: the withheld moves, in words", () => {
  const withheld = (reasons: WithheldReason[], fields: Record<string, string> = {}, state = "queue") =>
    item({
      name: itemName(20),
      type: desk.name,
      state,
      fields,
      withheld: reasons.map((reason, i) => ({ toState: `s${i}`, reason })),
    });
  test("OWN_REQUEST: You can't handle your own request", () => {
    expect(whyNot(desk, withheld([WithheldReason.OWN_REQUEST]), "user:sam", nameOf)).toEqual(["You can't handle your own request"]);
  });
  test("OWN_CONTAINER: You can't decide on your own request", () => {
    expect(whyNot(desk, withheld([WithheldReason.OWN_CONTAINER]), "user:sam", nameOf)).toEqual(["You can't decide on your own request"]);
  });
  test("CLAIMED_BY_OTHER: the holder has it, by name; Nobody holds it when the claim field is empty", () => {
    expect(whyNot(desk, withheld([WithheldReason.CLAIMED_BY_OTHER], { f_handler: "user:lea" }, "in_hand"), "user:sam", nameOf)).toEqual([
      "Lea has it",
    ]);
    expect(whyNot(desk, withheld([WithheldReason.CLAIMED_BY_OTHER], {}, "in_hand"), "user:sam", nameOf)).toEqual(["Nobody holds it"]);
  });
  test("each reason once, in the order first met; nothing withheld, nothing said", () => {
    const two = withheld([WithheldReason.CLAIMED_BY_OTHER, WithheldReason.OWN_REQUEST, WithheldReason.CLAIMED_BY_OTHER], { f_handler: "user:lea" }, "in_hand");
    expect(whyNot(desk, two, "user:sam", nameOf)).toEqual(["Lea has it", "You can't handle your own request"]);
    expect(whyNot(desk, withheld([]), "user:sam", nameOf)).toEqual([]);
  });
});

describe("inboxSearch: a team tab's claimed rows", () => {
  const team: InboxTab = { key: "role:desk_agent", label: "Desk agent", waitingOn: "role:desk_agent", role: "desk_agent" };
  test("claimed: the same search, with filter.claimed", () => {
    expect(inboxSearch({ parent: "namespaces/acme", tab: team, order: "oldest", place: "", claimed: true })).toEqual({
      parent: "namespaces/acme",
      pageSize: 50,
      orderBy: "waiting_since",
      filter: { waitingOn: "role:desk_agent", claimed: true },
    });
  });
  test("with a place, and the next page", () => {
    expect(
      inboxSearch({ parent: "namespaces/acme", tab: team, order: "newest", place: itemName(77), claimed: true }, { pageToken: "p2" }),
    ).toEqual({
      parent: "namespaces/acme",
      pageSize: 50,
      orderBy: "waiting_since desc",
      filter: { waitingOn: "role:desk_agent", in: itemName(77), claimed: true },
      pageToken: "p2",
    });
  });
});
