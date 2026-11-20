// The etag step: a save never includes a field the caller cannot write,
// whatever the form holds, and the form is rebuilt whenever what it shows
// changed.
//
// Why: the etag no longer changes when the kernel writes a field only it
// writes (an incident's due instants). A page that was not rebuilt after
// such a write holds the old value of that field; sent with a save, it
// would be refused FIELD_NOT_WRITABLE.
import { describe, expect, test } from "vitest";
import { item, itemName, leaveBalance, story2, story3 } from "../test/fixtures";
import { formKey, formValues, patchFields } from "./values";

describe("patchFields never sends a field the caller cannot write", () => {
  const balance = { f_owner: "user:ana", f_allowance: "25", f_used: "3" };

  test("a kernel-written field the form holds another value for is left out", () => {
    const form = { ...formValues(leaveBalance, balance), f_used: "9" };
    expect(patchFields(leaveBalance, balance, form, ["hr_admin"])).toEqual({});
    expect(patchFields(leaveBalance, balance, { ...form, f_allowance: "30" }, ["hr_admin"])).toEqual({ f_allowance: "30" });
  });

  test("the kernel wrote a date-time after the form was built: the old value is not sent, nor a null", () => {
    // The form was built before the story was resolved; the page then read
    // the Item again and was not rebuilt.
    const before = { f_summary: "Log in" };
    const after = { f_summary: "Log in", f_resolved_at: "2026-11-19T17:00:00.123456Z" };
    const form = formValues(story2, before);
    expect(patchFields(story2, after, form)).toEqual({});
    expect(patchFields(story2, after, { ...form, f_summary: "Log in with SSO" })).toEqual({ f_summary: "Log in with SSO" });
    // And the other way round: the kernel cleared it meanwhile.
    expect(patchFields(story2, before, formValues(story2, after))).toEqual({});
  });

  test("a kernel-written set is never sent as a set operation", () => {
    const stored = { f_summary: "Log in", f_stamps: ["a", "b"] };
    const form = { ...formValues(story3, stored), f_stamps: ["a"], f_labels: ["api"] };
    expect(patchFields(story3, stored, form)).toEqual({ f_labels: { add: ["api"] } });
  });

  test("a role-written field is sent only by a holder of one of its roles", () => {
    const form = { ...formValues(leaveBalance, balance), f_allowance: "30" };
    expect(patchFields(leaveBalance, balance, form, ["hr_admin"])).toEqual({ f_allowance: "30" });
    expect(patchFields(leaveBalance, balance, form, ["approver", "hr_admin"])).toEqual({ f_allowance: "30" });
    expect(patchFields(leaveBalance, balance, form, [])).toEqual({});
    expect(patchFields(leaveBalance, balance, form, ["approver"])).toEqual({});
  });

  test("roles not known: a role-written field is sent, and the server decides", () => {
    const form = { ...formValues(leaveBalance, balance), f_allowance: "30", f_used: "9" };
    expect(patchFields(leaveBalance, balance, form)).toEqual({ f_allowance: "30" });
  });
});

describe("formKey", () => {
  const shown = (fields: Record<string, string>, more: { etag?: string; state?: string } = {}) =>
    item({ name: itemName(40), type: story2.name, state: "todo", etag: "c3", fields, ...more });
  const base = shown({ f_summary: "Log in" });

  test("the same Item read again has the same key: what was typed is kept", () => {
    expect(formKey(shown({ f_summary: "Log in" }))).toBe(formKey(base));
  });

  test("a kernel-written field changed and the etag did not: a new key", () => {
    const resolved = shown({ f_summary: "Log in", f_resolved_at: "2026-11-19T17:00:00.123456Z" });
    expect(resolved.etag).toBe(base.etag);
    expect(formKey(resolved)).not.toBe(formKey(base));
    const later = shown({ f_summary: "Log in", f_resolved_at: "2026-11-20T09:00:00.000000Z" });
    expect(formKey(later)).not.toBe(formKey(resolved));
  });

  test("the etag or the state changed: a new key", () => {
    expect(formKey(shown({ f_summary: "Log in" }, { etag: "c4" }))).not.toBe(formKey(base));
    expect(formKey(shown({ f_summary: "Log in" }, { state: "done" }))).not.toBe(formKey(base));
  });

  test("the order the fields arrive in does not matter", () => {
    const a = shown({ f_summary: "Log in", f_resolution: "fixed" });
    const b = shown({ f_resolution: "fixed", f_summary: "Log in" });
    expect(formKey(a)).toBe(formKey(b));
  });
});
