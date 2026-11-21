// @vitest-environment node
// Jira part 2, step 4a: the generic UI and customers.
//   - the picker offers the dev file's customers, labelled as such;
//   - a customer is offered only the Types a container admits (own and
//     shared, latest version), and nothing at the top level;
//   - a customer's transitions: only those whose allowed names creator are
//     offered as theirs (the server still decides).
import { readFileSync } from "node:fs";
import { create } from "@bufbuild/protobuf";
import { describe, expect, test } from "vitest";
import { TransitionSchema, TypeSchema, type Type } from "../gen/kernel/v1/kernel_pb";
import { creatableByCustomer, customerMayTake, isCustomer, pickerLabel } from "./customers";
import { parseDevUsers } from "./devusers";
import { NS } from "../test/fixtures";

const file = readFileSync(new URL("../../../dev-tokens.example.yaml", import.meta.url), "utf8");

const ty = (typeId: string, version: number, init: { own?: string[]; shared?: string[]; customerFields?: string[] } = {}): Type =>
  create(TypeSchema, { name: `${NS}/types/${typeId}/versions/${version}`, typeId, version, ...init });

const desk1 = ty("desk", 1);
const desk2 = ty("desk", 2, { own: ["incident"], customerFields: ["f_key", "f_name"] });
const incident1 = ty("incident", 1);
const incident2 = ty("incident", 2, { shared: ["reply"], customerFields: ["f_title", "f_priority"] });
const reply = ty("reply", 1, { customerFields: ["f_body"] });
const note = ty("note", 1);
const all = [desk1, desk2, incident1, incident2, reply, note];

describe("the picker", () => {
  test("offers the dev file's customers, labelled as customers", () => {
    const users = parseDevUsers(file);
    const kim = users.find((u) => u.name === "kim");
    expect(kim?.principal).toBe("customer:kim");
    expect(kim && isCustomer(kim)).toBe(true);
    expect(kim && pickerLabel(kim)).toBe("kim (customer)");
    const lea = users.find((u) => u.name === "lea")!;
    expect(isCustomer(lea)).toBe(false);
    expect(pickerLabel(lea)).toBe("lea");
  });
});

describe("what a customer may create", () => {
  test("in a portal: its own Types, latest version", () => {
    expect(creatableByCustomer(all, desk2).map((t) => t.name)).toEqual([incident2.name]);
  });
  test("in a request: its shared Types; never a note", () => {
    expect(creatableByCustomer(all, incident2).map((t) => t.name)).toEqual([reply.name]);
  });
  test("nothing at the top level, nor in a container that admits nobody", () => {
    expect(creatableByCustomer(all, undefined)).toEqual([]);
    expect(creatableByCustomer(all, desk1)).toEqual([]);
    expect(creatableByCustomer(all, incident1)).toEqual([]);
  });
});

describe("a customer's transitions", () => {
  test("only those whose allowed names creator", () => {
    const tr = (allowed: string[]) => create(TransitionSchema, { fromState: "new", toState: "canceled", allowed });
    expect(customerMayTake(tr(["creator", "role:desk_agent"]))).toBe(true);
    expect(customerMayTake(tr([]))).toBe(false);
    expect(customerMayTake(tr(["role:desk_agent"]))).toBe(false);
  });
});
