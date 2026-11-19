// Date-time fields in a form (Jira part 2, step 1). The server holds an
// instant in UTC; the form edits it on the browser's clock, to the second,
// and sends UTC back. Nothing here knows a Type: only the field's type.
import { afterAll, beforeAll, describe, expect, test } from "vitest";
import { FieldType } from "../gen/kernel/v1/kernel_pb";
import { sprint2, story2 } from "../test/fixtures";
import { createFields, filterValue, formValues, instantOf, localInput, patchFields } from "./values";

// A zone with daylight saving, west of UTC: New York is UTC-5 in December
// and UTC-4 in July.
let zone: string | undefined;
beforeAll(() => {
  zone = process.env.TZ;
  process.env.TZ = "America/New_York";
});
afterAll(() => {
  if (zone === undefined) delete process.env.TZ;
  else process.env.TZ = zone;
});

describe("localInput: a stored instant on the browser's clock", () => {
  test("in winter and in summer", () => {
    expect(localInput("2026-12-04T17:00:00.000000Z")).toBe("2026-12-04T12:00:00");
    expect(localInput("2026-07-01T17:00:00.000000Z")).toBe("2026-07-01T13:00:00");
  });

  test("across midnight: the local date differs from the UTC date", () => {
    expect(localInput("2026-12-05T03:30:15.000000Z")).toBe("2026-12-04T22:30:15");
  });

  test("to the second: a fraction is not shown", () => {
    expect(localInput("2026-12-04T17:00:00.123456Z")).toBe("2026-12-04T12:00:00");
  });

  test("anything that is not an instant is empty", () => {
    expect(localInput("")).toBe("");
    expect(localInput("soon")).toBe("");
  });
});

describe("instantOf: what a datetime-local input sends", () => {
  test("UTC, with an offset the server accepts", () => {
    expect(instantOf("2026-12-04T12:00:00")).toBe("2026-12-04T17:00:00.000Z");
    expect(instantOf("2026-07-01T13:00:00")).toBe("2026-07-01T17:00:00.000Z");
  });

  test("a browser may leave the seconds out", () => {
    expect(instantOf("2026-12-04T12:00")).toBe("2026-12-04T17:00:00.000Z");
  });

  test("an empty or unreadable input is empty", () => {
    expect(instantOf("")).toBe("");
    expect(instantOf("soon")).toBe("");
  });

  test("there and back", () => {
    for (const stored of ["2026-12-04T17:00:00.000000Z", "2026-03-08T06:59:59.000000Z", "2026-11-01T05:30:00.000000Z"]) {
      expect(instantOf(localInput(stored))).toBe(stored.replace(".000000Z", ".000Z"));
    }
  });
});

describe("a form with a datetime field", () => {
  const fields = { f_name: "Sprint 12", f_end: "2026-12-04", f_end_at: "2026-12-04T17:00:00.000000Z" };

  test("formValues shows the instant on the browser's clock; the date is left alone", () => {
    expect(formValues(sprint2, fields)).toEqual({
      f_name: "Sprint 12",
      f_end: "2026-12-04",
      f_end_at: "2026-12-04T12:00:00",
    });
    expect(formValues(sprint2, undefined).f_end_at).toBe("");
  });

  test("createFields sends UTC", () => {
    const form = { ...formValues(sprint2, undefined), f_name: "Sprint 13", f_end_at: "2026-12-18T12:00" };
    expect(createFields(sprint2, form)).toEqual({ f_name: "Sprint 13", f_end_at: "2026-12-18T17:00:00.000Z" });
  });

  test("patchFields sends nothing for an instant nobody touched", () => {
    expect(patchFields(sprint2, fields, formValues(sprint2, fields))).toEqual({});
  });

  test("patchFields sends nothing for an untouched instant with microseconds: it is not truncated", () => {
    const stamped = { f_summary: "Log in", f_resolved_at: "2026-12-04T17:00:00.123456Z" };
    const form = { ...formValues(story2, stamped), f_summary: "Log in with SSO" };
    expect(patchFields(story2, stamped, form)).toEqual({ f_summary: "Log in with SSO" });
  });

  test("patchFields sends a moved instant in UTC, and null for an emptied one", () => {
    const form = formValues(sprint2, fields);
    expect(patchFields(sprint2, fields, { ...form, f_end_at: "2026-12-11T09:30:00" })).toEqual({
      f_end_at: "2026-12-11T14:30:00.000Z",
    });
    expect(patchFields(sprint2, fields, { ...form, f_end_at: "" })).toEqual({ f_end_at: null });
  });

  test("a filter's value is the text typed: an instant with its offset", () => {
    expect(filterValue(FieldType.DATETIME, "2026-12-04T18:00:00+01:00")).toBe("2026-12-04T18:00:00+01:00");
  });
});
