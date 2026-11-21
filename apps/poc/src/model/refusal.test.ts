// The server's refusals in plain words. The sentence never invents a
// refusal: it is chosen by the reason the server sent.
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { BadRequestSchema, ErrorInfoSchema } from "@gen/google/rpc/error_details_pb";
import { describe, expect, test } from "vitest";
import { explain, refusalOf, type Refusal } from "./refusal";

const DOMAIN = "pli.francoisnyc.github.io";

function connectError(code: Code, reason: string, metadata: Record<string, string> = {}): ConnectError {
  return new ConnectError("the fixed message", code, undefined, [
    { desc: ErrorInfoSchema, value: create(ErrorInfoSchema, { reason, domain: DOMAIN, metadata }) },
  ]);
}

const refusal = (code: string, reason: string, metadata: Record<string, string> = {}): Refusal => ({
  code,
  reason,
  message: "the fixed message",
  metadata,
  violations: [],
});

describe("refusalOf", () => {
  test("reads the code, the reason and the metadata of the item service's ErrorInfo", () => {
    const r = refusalOf(connectError(Code.FailedPrecondition, "MISSING_FIELDS", { fields: "f_resolution" }));
    expect(r).toEqual(refusal("FAILED_PRECONDITION", "MISSING_FIELDS", { fields: "f_resolution" }));
  });

  test("ignores an ErrorInfo of another domain", () => {
    const err = new ConnectError("x", Code.Internal, undefined, [
      { desc: ErrorInfoSchema, value: create(ErrorInfoSchema, { reason: "OTHER", domain: "example.com" }) },
    ]);
    expect(refusalOf(err).reason).toBe("");
  });

  test("reads field violations", () => {
    const bad = create(BadRequestSchema, { fieldViolations: [{ field: "fields.f_title", description: "must be a string" }] });
    const err = new ConnectError("invalid", Code.InvalidArgument, undefined, [{ desc: BadRequestSchema, value: bad }]);
    expect(refusalOf(err).violations).toEqual([{ field: "fields.f_title", description: "must be a string" }]);
  });

  test("anything else is UNKNOWN with its message", () => {
    expect(refusalOf(new TypeError("Failed to fetch"))).toEqual({
      code: "UNKNOWN",
      reason: "",
      message: "Failed to fetch",
      metadata: {},
      violations: [],
    });
  });
});

describe("explain", () => {
  test.each<[string, Refusal, string]>([
    ["a missing field, by its name", refusal("FAILED_PRECONDITION", "MISSING_FIELDS", { fields: "f_resolution" }), "Fill in resolution first."],
    [
      "two missing fields",
      refusal("FAILED_PRECONDITION", "MISSING_FIELDS", { fields: "f_resolution,f_assignee" }),
      "Fill in resolution and assignee first.",
    ],
    [
      "a stale write",
      refusal("ABORTED", "STALE"),
      "Someone changed this since you opened it. Reload to see their change, then try again.",
    ],
    ["no permission", refusal("PERMISSION_DENIED", "NOT_ALLOWED"), "You don't have permission to do that."],
    ["hidden or missing", refusal("NOT_FOUND", "NOT_FOUND"), "This doesn't exist, or you can't see it."],
    [
      "a frozen field, with the state's name",
      refusal("FAILED_PRECONDITION", "FIELD_FROZEN", { fields: "f_title", state: "resolved" }),
      "Title can't be changed while this is Resolved.",
    ],
    [
      "a field only someone else writes",
      refusal("PERMISSION_DENIED", "FIELD_NOT_WRITABLE", { fields: "f_due_resolution" }),
      "You can't change resolution due.",
    ],
    ["the incident's priority invariant", refusal("FAILED_PRECONDITION", "INVARIANT_FAILED", { invariant: "priority_missing" }), "Choose a priority."],
    ["another invariant", refusal("FAILED_PRECONDITION", "INVARIANT_FAILED", { invariant: "dates_in_order" }), "That would break one of this item's rules."],
    [
      "a max-one link already set",
      refusal("FAILED_PRECONDITION", "LINK_CARDINALITY", { link_type: "epic", side: "source" }),
      "This already has an epic. Remove that one first.",
    ],
    ["a link that exists", refusal("FAILED_PRECONDITION", "ALREADY_LINKED"), "These are already linked."],
    ["a held Item", refusal("ABORTED", "HELD", { hold: "x" }), "This is waiting on a check. Try again in a moment."],
    ["a guard, with no sentence from the screen", refusal("FAILED_PRECONDITION", "GUARD_FAILED"), "The rules for this move aren't met yet."],
    ["a bad token", refusal("UNAUTHENTICATED", "UNAUTHENTICATED"), "Your sign-in is no longer valid. Sign in again."],
    ["a search not caught up", refusal("UNAVAILABLE", "NOT_CAUGHT_UP"), "The server is catching up. Try again in a moment."],
    ["the network", refusal("UNKNOWN", ""), "Can't reach the server. Check your connection and try again."],
    ["an invalid request", refusal("INVALID_ARGUMENT", "INVALID_REQUEST"), "Some values aren't valid. See the details."],
    ["a reason the app does not know", refusal("FAILED_PRECONDITION", "SOMETHING_NEW"), "The server refused this (SOMETHING_NEW)."],
  ])("%s", (_what, r, sentence) => {
    expect(explain(r)).toBe(sentence);
  });

  test("a screen's sentence for its guard wins", () => {
    const guard = "An epic is done only when all its issues are done or canceled.";
    expect(explain(refusal("FAILED_PRECONDITION", "GUARD_FAILED"), { guard })).toBe(guard);
  });

  test("states are named in the Type's words", () => {
    const r = refusal("FAILED_PRECONDITION", "FIELD_FROZEN", { fields: "f_summary", state: "in_review" });
    expect(explain(r, { typeId: "story" })).toBe("Summary can't be changed while this is In review.");
  });
});
