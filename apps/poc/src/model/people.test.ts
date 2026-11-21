// How people are named: there is no directory, a principal is its ID.
import { describe, expect, test } from "vitest";
import { authorLabel, initials, personName } from "./people";

describe("personName", () => {
  test.each([
    ["user:sam", "sam"],
    ["customer:kim", "kim"],
    ["agent:triage-bot", "triage-bot (agent)"],
    ["system:timer", "System"],
    ["reaction:incident:sla_answered", "System"],
    ["hook:incident:notify", "System"],
    ["", "Someone"],
  ])("%s is %s", (principal, name) => {
    expect(personName(principal)).toBe(name);
  });
});

describe("initials", () => {
  test.each([
    ["user:sam", "S"],
    ["customer:kim", "K"],
    ["agent:triage-bot", "TB"],
    ["user:mary_jane", "MJ"],
  ])("%s is %s", (principal, letters) => {
    expect(initials(principal)).toBe(letters);
  });
});

describe("authorLabel", () => {
  test("a customer sees themselves as You and staff as Support", () => {
    expect(authorLabel("customer:kim", "customer:kim", "customer")).toBe("You");
    expect(authorLabel("user:sam", "customer:kim", "customer")).toBe("sam · Support");
  });

  test("staff see a customer as Customer, a colleague by name, themselves as You", () => {
    expect(authorLabel("customer:kim", "user:sam", "staff")).toBe("kim · Customer");
    expect(authorLabel("user:lea", "user:sam", "staff")).toBe("lea");
    expect(authorLabel("user:sam", "user:sam", "staff")).toBe("You");
  });
});
