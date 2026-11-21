// The words the apps use for their Types.
import { StateCategory } from "@gen/kernel/v1/kernel_pb";
import { describe, expect, test } from "vitest";
import {
  categoryTone,
  fieldLabel,
  issueKey,
  optionLabel,
  stateLabel,
  transitionLabel,
  typeIdOf,
  urgencyChoices,
  versionOf,
} from "./labels";

test("a Type name's ID and version", () => {
  expect(typeIdOf("namespaces/acme/types/story/versions/3")).toBe("story");
  expect(versionOf("namespaces/acme/types/story/versions/3")).toBe(3);
  expect(versionOf("namespaces/acme/types/story")).toBe(0);
});

describe("stateLabel", () => {
  test.each([
    ["story", "in_review", "In review"],
    ["bug", "todo", "To do"],
    ["task", "canceled", "Canceled"],
    ["epic", "in_progress", "In progress"],
    ["sprint", "active", "Active"],
    ["incident", "new", "New"],
    ["incident", "waiting_for_customer", "Waiting for customer"],
    ["sla_goal", "paused", "Paused"],
    ["anything", "on_hold", "On hold"],
  ])("%s %s is %s to staff", (typeId, state, label) => {
    expect(stateLabel(typeId, state)).toBe(label);
    expect(stateLabel(typeId, state, "staff")).toBe(label);
  });

  test.each([
    ["new", "Received"],
    ["in_progress", "In progress"],
    ["waiting_for_customer", "Waiting for you"],
    ["resolved", "Resolved"],
    ["closed", "Closed"],
    ["canceled", "Canceled"],
  ])("a customer's incident %s is %s", (state, label) => {
    expect(stateLabel("incident", state, "customer")).toBe(label);
  });
});

test.each([
  ["f_summary", "Summary"],
  ["f_title", "Title"],
  ["f_assignee", "Assignee"],
  ["f_resolution", "Resolution"],
  ["f_due_first_response", "First response due"],
  ["f_due_resolution", "Resolution due"],
  ["f_resolved_at", "Resolved at"],
  ["f_points", "Points"],
  ["f_labels", "Labels"],
  ["f_some_new_thing", "Some new thing"],
])("field %s is %s", (id, label) => {
  expect(fieldLabel(id)).toBe(label);
});

test.each([
  ["f_resolution", "cannot_reproduce", "Cannot reproduce"],
  ["f_resolution", "wontfix", "Won't fix"],
  ["f_impact", "extensive", "Extensive"],
  ["f_priority", "P1", "P1"],
])("option %s %s is %s", (field, option, label) => {
  expect(optionLabel(field, option)).toBe(label);
});

describe("transitionLabel", () => {
  test.each([
    ["story", "todo", "in_progress", "Start progress"],
    ["bug", "in_progress", "in_review", "Send to review"],
    ["task", "in_review", "in_progress", "Back to progress"],
    ["story", "in_review", "done", "Mark done"],
    ["story", "done", "todo", "Reopen"],
    ["story", "todo", "canceled", "Cancel"],
    ["story", "canceled", "todo", "Restore"],
    ["epic", "todo", "in_progress", "Start"],
    ["epic", "in_progress", "done", "Mark done"],
    ["sprint", "planned", "active", "Start sprint"],
    ["sprint", "active", "closed", "Complete sprint"],
    ["incident", "new", "in_progress", "Start work"],
    ["incident", "in_progress", "waiting_for_customer", "Wait for customer"],
    ["incident", "waiting_for_customer", "in_progress", "Resume work"],
    ["incident", "in_progress", "resolved", "Resolve"],
    ["incident", "resolved", "in_progress", "Reopen"],
    ["incident", "new", "canceled", "Cancel"],
    ["thing", "a", "on_hold", "Move to On hold"],
  ])("%s %s -> %s is %s", (typeId, from, to, label) => {
    expect(transitionLabel(typeId, from, to)).toBe(label);
  });

  test("a customer's own moves", () => {
    expect(transitionLabel("incident", "new", "canceled", "customer")).toBe("Cancel request");
    expect(transitionLabel("incident", "resolved", "in_progress", "customer")).toBe("Reopen request");
  });
});

test("the portal asks for urgency in plain words, P1 to P4", () => {
  expect(urgencyChoices.map((c) => c.value)).toEqual(["P1", "P2", "P3", "P4"]);
  for (const c of urgencyChoices) expect(c.label).toMatch(/^[A-Z][a-z]+ — /);
});

test.each([
  [StateCategory.TRIAGE, "neutral"],
  [StateCategory.BACKLOG, "neutral"],
  [StateCategory.TODO, "neutral"],
  [StateCategory.ACTIVE, "active"],
  [StateCategory.WAITING, "waiting"],
  [StateCategory.DONE, "done"],
  [StateCategory.CANCELED, "canceled"],
  [StateCategory.UNSPECIFIED, "neutral"],
])("category %s is drawn %s", (category, tone) => {
  expect(categoryTone(category)).toBe(tone);
});

// The LAST 8 characters: an Item ID is a UUIDv7, whose first ones are a
// timestamp, the same for every Item made in the same moment.
test("an issue's key is its space's key and the end of its ID", () => {
  expect(issueKey("PLAT", "namespaces/acme/items/01a1ff8c-0000-7000-8000-00003f9a1c2b")).toBe("PLAT · 3f9a1c2b");
  expect(issueKey("PLAT", "namespaces/acme/items/01a1ff8c-0000-7000-8000-0000aa11bb22")).toBe("PLAT · aa11bb22");
});
