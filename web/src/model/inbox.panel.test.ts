// @vitest-environment jsdom
// The Inbox for people, PR 2c: the rules behind the side panel, the
// selection bar and the keys. Pure functions; none decides who may act:
// the bar offers only moves every selected row's Item.moves lists.
//
// The owner's decisions (PR 2's Phase A, 2026-11-27): the bar offers a
// move only if every selected row lists it, all are the same Type, and it
// has no required fields; after a move the panel goes to the next row;
// J/K/Enter/X/P on the list, never while typing; the panel's URL is
// #/inbox?item=<id>.
import { create } from "@bufbuild/protobuf";
import { describe, expect, test } from "vitest";
import { FieldType, StateCategory, TypeSchema, type Type } from "../gen/kernel/v1/kernel_pb";
import { item, itemName, typeName } from "../test/fixtures";
import { bulkMoves, inboxKey, nextAfter, panelHash, panelOf } from "./inbox";

const desk = create(TypeSchema, {
  name: typeName("desk_request", 2),
  typeId: "desk_request",
  version: 2,
  fields: [
    { id: "f_title", type: FieldType.STRING },
    { id: "f_note", type: FieldType.STRING },
  ],
  states: [
    { id: "queue", category: StateCategory.WAITING },
    { id: "in_hand", category: StateCategory.ACTIVE, claim: "f_handler" },
    { id: "declined", category: StateCategory.CANCELED },
    { id: "parked", category: StateCategory.WAITING },
  ],
  transitions: [
    { fromState: "queue", toState: "in_hand", label: "Take it", primary: true },
    { fromState: "queue", toState: "declined", required: ["f_note"], label: "Decline" },
    { fromState: "queue", toState: "parked", label: "Park" },
  ],
});
const desk1 = create(TypeSchema, { ...desk, name: typeName("desk_request", 1), version: 1 });
const typeOf = (n: string): Type | undefined => (n === desk.name ? desk : n === desk1.name ? desk1 : undefined);
const row = (n: number, moves: string[], type = desk.name) => item({ name: itemName(n), type, state: "queue", moves });

describe("bulkMoves: what the selection bar offers", () => {
  test("a move every selected row lists, in the first row's order", () => {
    expect(bulkMoves([row(1, ["parked", "in_hand"]), row(2, ["in_hand", "parked"])], typeOf).map((t) => t.toState)).toEqual(["parked", "in_hand"]);
  });
  test("a move one row does not list is not offered", () => {
    expect(bulkMoves([row(1, ["in_hand", "parked"]), row(2, ["parked"])], typeOf).map((t) => t.toState)).toEqual(["parked"]);
  });
  test("a move with required fields is never offered", () => {
    expect(bulkMoves([row(1, ["declined", "in_hand"]), row(2, ["declined", "in_hand"])], typeOf).map((t) => t.toState)).toEqual(["in_hand"]);
  });
  test("rows of two Types, or two versions of one: nothing", () => {
    expect(bulkMoves([row(1, ["in_hand"]), row(2, ["in_hand"], desk1.name)], typeOf)).toEqual([]);
  });
  test("one row: its moves without required fields; no rows: nothing", () => {
    expect(bulkMoves([row(1, ["declined", "in_hand"])], typeOf).map((t) => t.label)).toEqual(["Take it"]);
    expect(bulkMoves([], typeOf)).toEqual([]);
  });
});

describe("nextAfter: where the panel goes after a move", () => {
  test("the next row of the list as it was, if it is still there", () => {
    expect(nextAfter(["a", "b", "c"], ["a", "c"], "b")).toBe("c");
    expect(nextAfter(["a", "b", "c", "d"], ["a", "d"], "b")).toBe("d");
  });
  test("the moved row is the last: the one before it", () => {
    expect(nextAfter(["a", "b", "c"], ["a", "b"], "c")).toBe("b");
  });
  test("the moved row is still listed (it moved within the view): the one after it", () => {
    expect(nextAfter(["a", "b", "c"], ["a", "b", "c"], "b")).toBe("c");
  });
  test("nothing left: none", () => {
    expect(nextAfter(["a"], [], "a")).toBeUndefined();
  });
});

describe("inboxKey", () => {
  const on = (tag: string, attrs: Record<string, string> = {}) => {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
    return el;
  };
  const link = on("a", { href: "#/inbox" });
  test("J, K, Enter, X and P on a row, either case", () => {
    expect(inboxKey({ key: "j", target: link })).toBe("next");
    expect(inboxKey({ key: "J", target: link })).toBe("next");
    expect(inboxKey({ key: "k", target: link })).toBe("prev");
    expect(inboxKey({ key: "Enter", target: link })).toBe("open");
    expect(inboxKey({ key: "x", target: link })).toBe("select");
    expect(inboxKey({ key: "p", target: link })).toBe("main");
  });
  test("on a row's checkbox or button too", () => {
    expect(inboxKey({ key: "j", target: on("input", { type: "checkbox" }) })).toBe("next");
    expect(inboxKey({ key: "k", target: on("button") })).toBe("prev");
  });
  test("never while typing: a text field, a textarea, a select, an editable element", () => {
    for (const target of [on("input"), on("input", { type: "text" }), on("input", { type: "search" }), on("textarea"), on("select"), on("div", { contenteditable: "true" })]) {
      expect(inboxKey({ key: "j", target })).toBeUndefined();
      expect(inboxKey({ key: "p", target })).toBeUndefined();
    }
  });
  test("never with Ctrl, Meta or Alt, and nothing for other keys", () => {
    expect(inboxKey({ key: "j", ctrlKey: true, target: link })).toBeUndefined();
    expect(inboxKey({ key: "p", metaKey: true, target: link })).toBeUndefined();
    expect(inboxKey({ key: "x", altKey: true, target: link })).toBeUndefined();
    expect(inboxKey({ key: "a", target: link })).toBeUndefined();
    expect(inboxKey({ key: "Escape", target: link })).toBeUndefined();
  });
});

describe("the panel's URL", () => {
  test("#/inbox?item=<id> with a row's panel open; #/inbox without", () => {
    expect(panelHash("0000-1")).toBe("#/inbox?item=0000-1");
    expect(panelHash("")).toBe("#/inbox");
  });
  test("read back: the item's ID, or nothing", () => {
    expect(panelOf("#/inbox?item=0000-1")).toBe("0000-1");
    expect(panelOf("#/inbox")).toBe("");
    expect(panelOf("")).toBe("");
    expect(panelOf("#/items/0000-1")).toBe("");
  });
});
