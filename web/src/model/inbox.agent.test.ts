// The Inbox for people, PR 3b: an agent's request, shown as one. Pure
// functions over what the kernel sends: Item.creator and creator_on_behalf_of,
// Item.state_actor and state_actor_on_behalf_of (PR 3a), and the Item's
// history (ItemEvent.actor and .fields).
//
// The owner's decisions (PR 3's Phase A, 2026-11-27): a row is an agent's
// request when an agent created the Item or put it in its state; the badge
// says whom it acted for (the latest on-behalf-of; "you" for the reader);
// the agent by its ID without "agent:"; "What the agent wrote" is the
// fields whose latest setting event was an agent's, a person's later
// setting taking it back.
import { create } from "@bufbuild/protobuf";
import { describe, expect, test } from "vitest";
import { FieldType, ItemEventKind, ItemEventSchema, StateCategory, TypeSchema, type ItemEvent } from "../gen/kernel/v1/kernel_pb";
import { item, itemName, typeName } from "../test/fixtures";
import { personName, type DevUser } from "./devusers";
import { agentBadge, agentLine, agentMark, agentWritten } from "./inbox";

const people: DevUser[] = [
  { name: "ana", token: "a", principal: "user:ana", namespace: "acme", roles: [] },
  { name: "bob", token: "b", principal: "user:bob", namespace: "acme", roles: [] },
  { name: "kim", token: "k", principal: "customer:kim", namespace: "acme", roles: [] },
];
const nameOf = (p: string) => personName(p, people);

const row = (init: { creator?: string; creatorOnBehalfOf?: string; stateActor?: string; stateActorOnBehalfOf?: string }) =>
  item({ name: itemName(1), type: typeName("ticket", 9), state: "in_progress", ...init });

describe("agentMark: is it an agent's request, and for whom", () => {
  test("a person created it and a person moved it: no", () => {
    expect(agentMark(row({ creator: "user:ana", stateActor: "user:bob" }), "user:bob", nameOf)).toBeUndefined();
  });
  test("an agent put it in its state, for a user: the agent, for that user by name", () => {
    expect(agentMark(row({ creator: "user:ana", stateActor: "agent:triage-bot", stateActorOnBehalfOf: "user:ana" }), "user:bob", nameOf)).toEqual({
      agent: "triage-bot",
      forWhom: "Ana",
    });
  });
  test("an agent created it, a person moved it since: still the agent's request, for whom it created it", () => {
    expect(agentMark(row({ creator: "agent:portal-bot", creatorOnBehalfOf: "customer:kim", stateActor: "user:bob" }), "user:bob", nameOf)).toEqual({
      agent: "portal-bot",
      forWhom: "Kim",
    });
  });
  test("an agent created it and another moved it: the latest, the one that moved it", () => {
    expect(
      agentMark(
        row({ creator: "agent:portal-bot", creatorOnBehalfOf: "customer:kim", stateActor: "agent:triage-bot", stateActorOnBehalfOf: "user:ana" }),
        "user:bob",
        nameOf,
      ),
    ).toEqual({ agent: "triage-bot", forWhom: "Ana" });
  });
  test("for the reader: you", () => {
    expect(agentMark(row({ creator: "agent:portal-bot", creatorOnBehalfOf: "customer:kim", stateActor: "agent:portal-bot", stateActorOnBehalfOf: "customer:kim" }), "customer:kim", nameOf)).toEqual({
      agent: "portal-bot",
      forWhom: "you",
    });
  });
  test("an agent on its own: for nobody; someone the dev file lacks: their ID", () => {
    expect(agentMark(row({ creator: "user:ana", stateActor: "agent:sweeper" }), "user:bob", nameOf)).toEqual({ agent: "sweeper", forWhom: "" });
    expect(agentMark(row({ creator: "agent:helper", creatorOnBehalfOf: "user:zoe", stateActor: "agent:helper", stateActorOnBehalfOf: "user:zoe" }), "user:bob", nameOf)).toEqual({
      agent: "helper",
      forWhom: "user:zoe",
    });
  });
  test("the kernel's own actors are not agents", () => {
    expect(agentMark(row({ creator: "user:ana", stateActor: "system:timer" }), "user:bob", nameOf)).toBeUndefined();
  });
});

describe("the badge and the panel's line", () => {
  test("Agent request · for Ana; Agent request alone when it acted for nobody", () => {
    expect(agentBadge({ agent: "triage-bot", forWhom: "Ana" })).toBe("Agent request · for Ana");
    expect(agentBadge({ agent: "portal-bot", forWhom: "you" })).toBe("Agent request · for you");
    expect(agentBadge({ agent: "sweeper", forWhom: "" })).toBe("Agent request");
  });
  test("triage-bot, for Ana; the agent alone when it acted for nobody", () => {
    expect(agentLine({ agent: "triage-bot", forWhom: "Ana" })).toBe("triage-bot, for Ana");
    expect(agentLine({ agent: "sweeper", forWhom: "" })).toBe("sweeper");
  });
});

describe("agentWritten: what the agent wrote", () => {
  const type = create(TypeSchema, {
    name: typeName("ticket", 9),
    typeId: "ticket",
    version: 9,
    fields: [
      { id: "f_title", type: FieldType.STRING, label: "Title" },
      { id: "f_priority", type: FieldType.INT },
      { id: "f_note", type: FieldType.STRING, label: "Note" },
      { id: "f_owner", type: FieldType.PRINCIPAL, label: "Owner" },
    ],
    states: [{ id: "in_progress", category: StateCategory.ACTIVE }],
  });
  const ev = (seq: number, actor: string, fields: Record<string, unknown>): ItemEvent =>
    create(ItemEventSchema, {
      name: `${itemName(1)}/events/${seq}`,
      itemSeq: BigInt(seq),
      kind: seq === 1 ? ItemEventKind.CREATED : ItemEventKind.UPDATED,
      actor,
      fields: fields as never,
    });
  const now = (fields: Record<string, string | number>) => item({ name: itemName(1), type: type.name, state: "in_progress", fields });

  test("the fields an agent set last, with their value now, in the Type's order; a label, else the ID", () => {
    const events = [ev(1, "user:ana", { f_title: "Printer" }), ev(2, "agent:triage-bot", { f_priority: 2, f_note: "Seen twice\nthis week" })];
    expect(agentWritten(type, now({ f_title: "Printer", f_priority: 2, f_note: "Seen twice\nthis week" }), events, nameOf)).toEqual([
      { field: "f_priority", label: "f_priority", value: "2" },
      { field: "f_note", label: "Note", value: "Seen twice\nthis week" },
    ]);
  });
  test("a person who set the field after the agent takes it back", () => {
    const events = [ev(1, "agent:triage-bot", { f_title: "Printer", f_note: "agent's" }), ev(2, "user:bob", { f_note: "bob's" })];
    expect(agentWritten(type, now({ f_title: "Printer", f_note: "bob's" }), events, nameOf)).toEqual([{ field: "f_title", label: "Title", value: "Printer" }]);
  });
  test("an agent that set a field after a person: the agent's again", () => {
    const events = [ev(1, "user:ana", { f_note: "ana's" }), ev(2, "agent:triage-bot", { f_note: "the agent's" })];
    expect(agentWritten(type, now({ f_note: "the agent's" }), events, nameOf)).toEqual([{ field: "f_note", label: "Note", value: "the agent's" }]);
  });
  test("a field the agent cleared is left out; a person named is named", () => {
    const events = [ev(1, "agent:triage-bot", { f_note: null, f_owner: "user:ana" })];
    expect(agentWritten(type, now({ f_owner: "user:ana" }), events, nameOf)).toEqual([{ field: "f_owner", label: "Owner", value: "Ana" }]);
  });
  test("no agent in the history: nothing; the text is kept exactly as written", () => {
    expect(agentWritten(type, now({ f_note: "x" }), [ev(1, "user:ana", { f_note: "x" })], nameOf)).toEqual([]);
    const raw = "<b>x</b> [a](javascript:alert(1)) **bold**";
    expect(agentWritten(type, now({ f_note: raw }), [ev(1, "agent:triage-bot", { f_note: raw })], nameOf)).toEqual([
      { field: "f_note", label: "Note", value: raw },
    ]);
  });
});
