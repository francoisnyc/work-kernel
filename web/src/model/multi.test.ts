// Multi-value fields in a form (Jira part 2, step 2). The wire holds a set
// as a sorted JSON array. A form holds its members; an edit is sent as a
// set operation ({add, remove}), never as a replacement, so two people
// editing one Item's labels do not overwrite each other.
import { describe, expect, test } from "vitest";
import { FieldType } from "../gen/kernel/v1/kernel_pb";
import { story3 } from "../test/fixtures";
import { createFields, filterValue, formValues, patchFields } from "./values";

const stored = { f_summary: "Log in", f_labels: ["backend", "sso"], f_reviewers: ["user:sam"] };

describe("formValues", () => {
  test("a set is its members; an unset one is empty", () => {
    expect(formValues(story3, stored)).toEqual({
      f_summary: "Log in",
      f_labels: ["backend", "sso"],
      f_colors: [],
      f_reviewers: ["user:sam"],
      f_days: [],
      f_stamps: [],
    });
    expect(formValues(story3, undefined).f_labels).toEqual([]);
  });
});

describe("createFields", () => {
  test("a new Item gets the whole set, sorted; an empty set is not sent", () => {
    const form = { ...formValues(story3, undefined), f_summary: "New", f_labels: ["sso", "api"] };
    expect(createFields(story3, form)).toEqual({ f_summary: "New", f_labels: ["api", "sso"] });
  });
});

describe("patchFields", () => {
  const form = formValues(story3, stored);

  test("nothing for a set nobody touched", () => {
    expect(patchFields(story3, stored, form)).toEqual({});
  });

  test("members added and removed are a set operation, not a new list", () => {
    expect(patchFields(story3, stored, { ...form, f_labels: ["api", "backend", "urgent"] })).toEqual({
      f_labels: { add: ["api", "urgent"], remove: ["sso"] },
    });
  });

  test("only add, only remove", () => {
    expect(patchFields(story3, stored, { ...form, f_labels: ["backend", "sso", "ui"] })).toEqual({
      f_labels: { add: ["ui"] },
    });
    expect(patchFields(story3, stored, { ...form, f_reviewers: [] })).toEqual({
      f_reviewers: { remove: ["user:sam"] },
    });
  });

  test("the first members of an unset field are an add", () => {
    expect(patchFields(story3, stored, { ...form, f_colors: ["red"] })).toEqual({ f_colors: { add: ["red"] } });
  });

  test("the same members in another order are no change", () => {
    expect(patchFields(story3, stored, { ...form, f_labels: ["sso", "backend"] })).toEqual({});
  });

  test("a set and a plain field change together", () => {
    expect(patchFields(story3, stored, { ...form, f_summary: "Log in with SSO", f_labels: ["backend"] })).toEqual({
      f_summary: "Log in with SSO",
      f_labels: { remove: ["sso"] },
    });
  });
});

describe("filterValue", () => {
  test("a filter on a set takes one member: contains", () => {
    expect(filterValue(FieldType.STRING, "backend")).toBe("backend");
  });
});
