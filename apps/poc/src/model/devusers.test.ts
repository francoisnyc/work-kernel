// Who may sign in, and where each person lands.
import { readFileSync } from "node:fs";
import { describe, expect, test } from "vitest";
import { areasFor, groupUsers, homeFor, isCustomer, parseDevUsers, type DevUser } from "./devusers";

const file = readFileSync(new URL("../../../../dev-tokens.example.yaml", import.meta.url), "utf8");
const byName = (name: string): DevUser => parseDevUsers(file).find((u) => u.name === name)!;

describe("parseDevUsers", () => {
  test("reads the repository's file: users and customers in its order, agents left out", () => {
    expect(parseDevUsers(file).map((u) => u.name)).toEqual(["ana", "bob", "dana", "erin", "hana", "lea", "sam", "kim", "lee", "carol"]);
  });

  test("keeps each person's principal, namespace, token and roles", () => {
    expect(byName("lea")).toEqual({
      name: "lea",
      token: "dev-lea-not-a-secret",
      principal: "user:lea",
      namespace: "acme",
      roles: ["sla_admin", "desk_agent"],
    });
    expect(byName("kim").roles).toEqual([]);
  });

  test("refuses a file without a tokens list", () => {
    expect(() => parseDevUsers("grants: []")).toThrow(/tokens/);
    expect(() => parseDevUsers("::: not yaml")).toThrow(/tokens/);
  });
});

describe("groups and areas", () => {
  test("staff and customers apart, each in the file's order", () => {
    const { staff, customers } = groupUsers(parseDevUsers(file));
    expect(staff.map((u) => u.name)).toEqual(["ana", "bob", "dana", "erin", "hana", "lea", "sam", "carol"]);
    expect(customers.map((u) => u.name)).toEqual(["kim", "lee"]);
  });

  test("a customer gets the portal only", () => {
    expect(isCustomer(byName("kim"))).toBe(true);
    expect(areasFor(byName("kim"))).toEqual(["portal"]);
    expect(homeFor(byName("kim"))).toBe("#/portal");
  });

  test("a desk agent gets the desk first, then the tracker", () => {
    expect(areasFor(byName("sam"))).toEqual(["desk", "tracker"]);
    expect(homeFor(byName("sam"))).toBe("#/desk");
    expect(areasFor(byName("lea"))).toEqual(["desk", "tracker"]);
  });

  test("other staff get the tracker", () => {
    expect(isCustomer(byName("ana"))).toBe(false);
    expect(areasFor(byName("ana"))).toEqual(["tracker"]);
    expect(homeFor(byName("ana"))).toBe("#/tracker");
  });
});
