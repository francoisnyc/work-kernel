// The conversation of an incident: replies and notes, never mixed up.
import { describe, expect, test } from "vitest";
import { Kind, event, goal, item, typeName } from "../test/fixtures";
import { messagesOf, portalThread } from "./conversation";

const reply = (id: string, at: string, body: string, creator = "user:sam") =>
  item(id, typeName("reply", 1), { at, fields: { f_body: body }, creator, container: "namespaces/acme/items/inc" });
const note = (id: string, at: string, body: string) =>
  item(id, typeName("note", 1), { at, fields: { f_body: body }, container: "namespaces/acme/items/inc" });

describe("messagesOf", () => {
  test("replies and notes, oldest first, each with its kind; goals and the rest left out", () => {
    const children = [
      note("n1", "2026-11-21T10:00:00Z", "check the logs"),
      reply("r1", "2026-11-21T09:00:00Z", "it is down", "customer:kim"),
      goal("g1", "inc", "running", { f_metric: "resolution" }),
      reply("r2", "2026-11-21T11:00:00Z", "we are on it"),
    ];
    expect(messagesOf(children)).toEqual([
      { name: "namespaces/acme/items/r1", kind: "reply", author: "customer:kim", at: "2026-11-21T09:00:00.000Z", body: "it is down" },
      { name: "namespaces/acme/items/n1", kind: "note", author: "user:sam", at: "2026-11-21T10:00:00.000Z", body: "check the logs" },
      { name: "namespaces/acme/items/r2", kind: "reply", author: "user:sam", at: "2026-11-21T11:00:00.000Z", body: "we are on it" },
    ]);
  });

  test("same instant: by name, so the order is stable", () => {
    const at = "2026-11-21T09:00:00Z";
    expect(messagesOf([reply("b", at, "2"), reply("a", at, "1")]).map((m) => m.body)).toEqual(["1", "2"]);
  });

  test("a PLAT comment is a comment", () => {
    const c = item("c1", typeName("comment", 2), { fields: { f_body: "looks good" } });
    expect(messagesOf([c])[0]?.kind).toBe("comment");
  });
});

describe("portalThread", () => {
  test("the replies and the status changes, by time; the creation is not a status change", () => {
    const events = [
      event(Kind.CREATED, "2026-11-21T08:59:00Z", { actor: "customer:kim", toState: "new" }),
      event(Kind.TRANSITIONED, "2026-11-21T09:30:00Z", { fromState: "new", toState: "in_progress" }),
      event(Kind.UPDATED, "2026-11-21T09:40:00Z", { fields: { f_priority: "P2" } }),
      event(Kind.TRANSITIONED, "2026-11-21T10:30:00Z", { fromState: "in_progress", toState: "waiting_for_customer" }),
    ];
    const thread = portalThread(
      [reply("r2", "2026-11-21T10:00:00Z", "we are on it"), reply("r1", "2026-11-21T09:00:00Z", "it is down", "customer:kim")],
      events,
    );
    expect(thread.map((e) => (e.kind === "status" ? `status ${e.state}` : `message ${e.message.body}`))).toEqual([
      "message it is down",
      "status in_progress",
      "message we are on it",
      "status waiting_for_customer",
    ]);
  });
});
