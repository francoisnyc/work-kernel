import { describe, expect, test } from "vitest";
import { FieldType } from "../gen/kernel/v1/kernel_pb";
import { aTicket, ticket, vacation } from "../test/fixtures";
import { createFields, filterValue, formValues, patchFields } from "./values";

describe("formValues", () => {
  test("every declared field, unset ones empty", () => {
    expect(formValues(ticket, aTicket.fields)).toEqual({
      f_title: "printer on fire",
      f_priority: "2",
      f_reporter: "tom",
      f_assignee: "",
      f_urgent: true,
      f_resolution: "",
      f_customer_id: "cust-good",
      f_amount: "",
      f_due: "",
    });
  });

  test("a new Item starts empty", () => {
    expect(formValues(vacation, undefined)).toEqual({
      f_start: "",
      f_end: "",
      f_days: "",
      f_leave_type: "",
      f_approver: "",
      f_comment: "",
    });
  });
});

describe("createFields", () => {
  test("sends only what was filled in, an int as a string", () => {
    const form = { ...formValues(ticket, undefined), f_title: "printer on fire", f_amount: "100", f_due: "2026-12-01" };
    expect(createFields(ticket, form)).toEqual({ f_title: "printer on fire", f_amount: "100", f_due: "2026-12-01" });
  });

  test("a ticked bool is true; an unticked one is not sent", () => {
    expect(createFields(ticket, { ...formValues(ticket, undefined), f_urgent: true })).toEqual({ f_urgent: true });
    expect(createFields(ticket, formValues(ticket, undefined))).toEqual({});
  });

  test("trims an int, a date, an enum and a principal; keeps a string as typed", () => {
    const form = { ...formValues(ticket, undefined), f_amount: " 7 ", f_assignee: " user:bob ", f_title: " two  spaces " };
    expect(createFields(ticket, form)).toEqual({ f_amount: "7", f_assignee: "user:bob", f_title: " two  spaces " });
  });
});

describe("patchFields", () => {
  const original = aTicket.fields;

  test("nothing changed: an empty patch", () => {
    expect(patchFields(ticket, original, formValues(ticket, original))).toEqual({});
  });

  test("only the changed fields", () => {
    const form = { ...formValues(ticket, original), f_title: "printer smoking", f_assignee: "user:bob" };
    expect(patchFields(ticket, original, form)).toEqual({ f_title: "printer smoking", f_assignee: "user:bob" });
  });

  test("an emptied field is null", () => {
    const form = { ...formValues(ticket, original), f_reporter: "", f_priority: "" };
    expect(patchFields(ticket, original, form)).toEqual({ f_reporter: null, f_priority: null });
  });

  test("unticking a true bool sends false; an unset bool left unticked is not sent", () => {
    expect(patchFields(ticket, original, { ...formValues(ticket, original), f_urgent: false })).toEqual({
      f_urgent: false,
    });
    const unset = { f_title: "x" };
    expect(patchFields(ticket, unset, formValues(ticket, unset))).toEqual({});
  });

  test("an int typed the same with spaces is not a change", () => {
    expect(patchFields(ticket, original, { ...formValues(ticket, original), f_priority: " 2 " })).toEqual({});
  });
});

describe("filterValue", () => {
  test("a bool takes true or false", () => {
    expect(filterValue(FieldType.BOOL, "true")).toBe(true);
    expect(filterValue(FieldType.BOOL, "false")).toBe(false);
  });

  test("everything else is the text: an int as a string, me for a principal", () => {
    expect(filterValue(FieldType.INT, "3")).toBe("3");
    expect(filterValue(FieldType.PRINCIPAL, "me")).toBe("me");
    expect(filterValue(FieldType.DATE, "2026-12-01")).toBe("2026-12-01");
    expect(filterValue(FieldType.ENUM, "P1")).toBe("P1");
  });
});
