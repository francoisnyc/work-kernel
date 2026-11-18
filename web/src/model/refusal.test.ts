import { Code, ConnectError } from "@connectrpc/connect";
import { expect, test } from "vitest";
import { BadRequestSchema, ErrorInfoSchema } from "../gen/google/rpc/error_details_pb";
import { refusal } from "../test/fixtures";
import { refusalOf } from "./refusal";

const DOMAIN = "pli.francoisnyc.github.io";

test("reads the code, the reason and the metadata", () => {
  const r = refusalOf(refusal(Code.FailedPrecondition, "MISSING_FIELDS", { fields: "f_resolution" }));
  expect(r.code).toBe("FAILED_PRECONDITION");
  expect(r.reason).toBe("MISSING_FIELDS");
  expect(r.metadata).toEqual({ fields: "f_resolution" });
  expect(r.message).toBe("the fixed message of this reason");
  expect(r.violations).toEqual([]);
});

test.each([
  [Code.PermissionDenied, "PERMISSION_DENIED", "FIELD_NOT_WRITABLE"],
  [Code.Aborted, "ABORTED", "STALE"],
  [Code.NotFound, "NOT_FOUND", "NOT_FOUND"],
  [Code.AlreadyExists, "ALREADY_EXISTS", "IDEMPOTENCY_KEY_REUSED"],
  [Code.Unauthenticated, "UNAUTHENTICATED", "UNAUTHENTICATED"],
  [Code.Unavailable, "UNAVAILABLE", "NOT_CAUGHT_UP"],
])("code %s prints as %s", (code, name, reason) => {
  const r = refusalOf(refusal(code, reason));
  expect(r.code).toBe(name);
  expect(r.reason).toBe(reason);
});

test("reads field violations of an invalid request", () => {
  const err = new ConnectError("invalid request", Code.InvalidArgument, undefined, [
    { desc: ErrorInfoSchema, value: { reason: "INVALID_REQUEST", domain: DOMAIN } },
    { desc: BadRequestSchema, value: { fieldViolations: [{ field: "fields.f_amount", description: "want a whole number" }] } },
  ]);
  const r = refusalOf(err);
  expect(r.reason).toBe("INVALID_REQUEST");
  expect(r.violations).toEqual([{ field: "fields.f_amount", description: "want a whole number" }]);
});

test("ignores an ErrorInfo of another domain", () => {
  const err = new ConnectError("from a proxy", Code.Unavailable, undefined, [
    { desc: ErrorInfoSchema, value: { reason: "SOMEONE_ELSES", domain: "example.com" } },
  ]);
  expect(refusalOf(err).reason).toBe("");
});

test("a Connect error without details has a code and no reason", () => {
  const r = refusalOf(new ConnectError("boom", Code.Internal));
  expect(r).toMatchObject({ code: "INTERNAL", reason: "", metadata: {}, violations: [] });
});

test("anything else is UNKNOWN, with its message", () => {
  expect(refusalOf(new TypeError("Failed to fetch"))).toMatchObject({ code: "UNKNOWN", reason: "", message: "Failed to fetch" });
  expect(refusalOf("just a string")).toMatchObject({ code: "UNKNOWN", reason: "" });
});
