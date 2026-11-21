// @vitest-environment node
// The fix of the 2026-11-21 review: a customer's Inbox runs no search on a
// field they cannot see. The Inbox is built from transitions whose allowed
// names field:X; a customer meets every Type through customerView, which
// must leave no such query on a field its customer_fields do not list, even
// on a move a customer may take.
import { create } from "@bufbuild/protobuf";
import { expect, test } from "vitest";
import { FieldType, TypeSchema } from "../gen/kernel/v1/kernel_pb";
import { customerView } from "./customers";
import { inboxQueries } from "./typeinfo";
import { NS } from "../test/fixtures";

const request = create(TypeSchema, {
  name: `${NS}/types/request/versions/1`,
  typeId: "request",
  version: 1,
  fields: [
    { id: "f_title", type: FieldType.STRING },
    { id: "f_owner", type: FieldType.PRINCIPAL },
    { id: "f_watcher", type: FieldType.PRINCIPAL },
  ],
  customerFields: ["f_title", "f_watcher"],
  transitions: [
    // A customer takes it (creator), and so does whoever f_owner names.
    { fromState: "open", toState: "closed", allowed: ["creator", "field:f_owner"] },
    // Staff only.
    { fromState: "open", toState: "held", allowed: ["field:f_owner"] },
    // A field the customer sees.
    { fromState: "open", toState: "seen", allowed: ["creator", "field:f_watcher"] },
  ],
});

test("a customer's Inbox has no query on a field they cannot see", () => {
  const queries = inboxQueries([customerView(request)]);
  expect(queries.map((q) => q.field)).not.toContain("f_owner");
});

test("a field the customer sees may still be asked", () => {
  expect(inboxQueries([customerView(request)])).toEqual([{ type: `${NS}/types/request`, state: "open", field: "f_watcher" }]);
});

test("staff: as before (one query per Type, state and field)", () => {
  expect(inboxQueries([request]).map((q) => q.field)).toEqual(["f_owner", "f_watcher"]);
});
