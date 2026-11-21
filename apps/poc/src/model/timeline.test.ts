// History as sentences.
import type { ItemRef } from "@gen/kernel/v1/kernel_pb";
import { describe, expect, test } from "vitest";
import { Kind, event, itemName } from "../test/fixtures";
import { describeEvent, type TimelineContext } from "./timeline";

const ctx: TimelineContext = {
  typeId: "story",
  audience: "staff",
  titleOf: (ref: ItemRef) => (ref.restricted ? "a hidden item" : ref.name === itemName("epic-1") ? "Single sign-on" : "Card B"),
};
const at = "2026-11-21T09:00:00Z";
const said = (ev: ReturnType<typeof event>, c: TimelineContext = ctx) => describeEvent(ev, c);

describe("people's changes", () => {
  test("created", () => {
    expect(said(event(Kind.CREATED, at, { actor: "user:lea", toState: "todo" }))).toEqual({
      at: "2026-11-21T09:00:00.000Z",
      actor: "lea",
      text: "created this",
      system: false,
    });
  });

  test("a move, in the Type's words", () => {
    expect(said(event(Kind.TRANSITIONED, at, { fromState: "todo", toState: "in_progress" }))?.text).toBe(
      "moved this from To do to In progress",
    );
  });

  test("a move to done that set fields says the people's fields only", () => {
    const ev = event(Kind.TRANSITIONED, at, {
      fromState: "in_review",
      toState: "done",
      fields: { f_resolution: "fixed", f_resolved_at: "2026-11-21T09:00:00.000000Z" },
    });
    expect(said(ev)?.text).toBe("moved this from In review to Done, set resolution to Fixed");
  });

  test("an update: set, cleared, a person, a date", () => {
    expect(said(event(Kind.UPDATED, at, { fields: { f_priority: "P1" } }))?.text).toBe("set priority to P1");
    expect(said(event(Kind.UPDATED, at, { fields: { f_resolution: null } }))?.text).toBe("cleared resolution");
    expect(said(event(Kind.UPDATED, at, { fields: { f_assignee: "user:sam" } }))?.text).toBe("set assignee to sam");
    expect(said(event(Kind.UPDATED, at, { fields: { f_priority: "P2", f_points: "3" } }))?.text).toBe(
      "set priority to P2, set points to 3",
    );
  });

  test("long text is not repeated in the history", () => {
    expect(said(event(Kind.UPDATED, at, { fields: { f_description: "A long story about the login page" } }))?.text).toBe(
      "changed description",
    );
  });

  test("a set operation says what was added and removed", () => {
    const ev = event(Kind.UPDATED, at, {
      fields: { f_labels: ["sso", "ui"] },
      setOps: { f_labels: { add: ["ui"], remove: ["old"] } },
    });
    expect(said(ev)?.text).toBe("added ui to labels, removed old from labels");
  });

  test("a rank move is said without its key", () => {
    expect(said(event(Kind.UPDATED, at, { fields: { f_rank: "0V7x" } }))?.text).toBe("changed the rank");
  });

  test("links: epic and sprint as membership, others by their name", () => {
    expect(said(event(Kind.LINK_ADDED, at, { linkType: "epic", linkTarget: itemName("epic-1") }))?.text).toBe(
      "added this to epic Single sign-on",
    );
    expect(said(event(Kind.LINK_REMOVED, at, { linkType: "sprint", linkTarget: itemName("s") }))?.text).toBe(
      "removed this from sprint Card B",
    );
    expect(said(event(Kind.LINK_ADDED, at, { linkType: "blocks", linkTarget: itemName("b") }))?.text).toBe(
      "linked this: blocks Card B",
    );
    expect(said(event(Kind.LINK_ADDED, at, { linkType: "relates", linkTarget: itemName("b") }))?.text).toBe(
      "linked this: relates to Card B",
    );
    expect(said(event(Kind.LINK_REMOVED, at, { linkType: "duplicates", linkTarget: itemName("b") }))?.text).toBe(
      "removed link: duplicates Card B",
    );
  });
});

describe("the kernel's own records", () => {
  test("a reaction's write is a system line", () => {
    const ev = event(Kind.UPDATED, at, {
      actor: "reaction:sla_goal:show_resolution",
      fields: { f_due_resolution: "2026-11-21T13:00:00.000000Z" },
    });
    expect(said(ev, { ...ctx, typeId: "incident" })).toEqual({
      at: "2026-11-21T09:00:00.000Z",
      actor: "System",
      text: "set resolution due to 2026-11-21 13:00 UTC",
      system: true,
    });
  });

  test("a timer's move is a system line", () => {
    const ev = event(Kind.TRANSITIONED, at, { actor: "system:timer", fromState: "resolved", toState: "closed" });
    expect(said(ev, { ...ctx, typeId: "incident" })).toMatchObject({ actor: "System", text: "moved this from Resolved to Closed", system: true });
  });

  test("reaction and hook records are system lines", () => {
    expect(said(event(Kind.REACTION_APPLIED, at, { reaction: "incident:sla_answered" }))).toMatchObject({
      text: "ran an automation (sla_answered)",
      system: true,
    });
    expect(said(event(Kind.REACTION_SKIPPED, at, { reaction: "incident:customer_replied" }))).toMatchObject({ system: true });
    expect(said(event(Kind.HOOK_RAN, at, { hook: "notify_breach" }))).toMatchObject({ text: "sent a notification", system: true });
  });

  test("a customer's history speaks their words", () => {
    const ev = event(Kind.TRANSITIONED, at, { fromState: "in_progress", toState: "waiting_for_customer" });
    expect(said(ev, { ...ctx, typeId: "incident", audience: "customer" })?.text).toBe("moved this from In progress to Waiting for you");
  });
});
