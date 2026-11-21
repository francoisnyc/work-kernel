// The backlog: a space's open issues outside the open sprints.
import { StateCategory } from "@gen/kernel/v1/kernel_pb";
import { describe, expect, test } from "vitest";
import { NS, item, itemName, typeName } from "../test/fixtures";
import { backlogOnly, inContainer, issueFilter, noFilters } from "./backlog";

const all = [`${NS}/types/story`, `${NS}/types/bug`, `${NS}/types/task`];
const open = [StateCategory.TODO, StateCategory.ACTIVE];
const str = (value: string) => ({ kind: { case: "stringValue" as const, value } });

describe("issueFilter", () => {
  test("no quick filter: the three issue Types, open", () => {
    expect(issueFilter(NS, noFilters)).toEqual({ types: all, categories: open });
  });

  test("mine, bugs only, P1, a label", () => {
    expect(issueFilter(NS, { mine: true, types: ["bug"], priority: "P1", label: "sso" })).toEqual({
      types: [`${NS}/types/bug`],
      categories: open,
      fields: [
        { field: "f_assignee", eq: str("me") },
        { field: "f_priority", eq: str("P1") },
        { field: "f_labels", eq: str("sso") },
      ],
    });
  });
});

describe("backlogOnly", () => {
  test("drops the issues an open sprint holds, keeps the order", () => {
    const a = item("a", typeName("story", 3));
    const b = item("b", typeName("bug", 3));
    const c = item("c", typeName("task", 3));
    expect(backlogOnly([a, b, c], new Set([b.name])).map((i) => i.name)).toEqual([a.name, c.name]);
  });
});

test("inContainer keeps one space's Items", () => {
  const here = item("a", typeName("story", 3), { container: itemName("plat") });
  const there = item("b", typeName("story", 3), { container: itemName("other") });
  expect(inContainer([here, there], itemName("plat"))).toEqual([here]);
});
