// The Requests area's logic on the vacation request and the leave balance:
// a balance in numbers, a person's requests, the form, sending it, the
// approver's decision, and every refusal in plain words. The app judges
// no guard and no invariant: it explains what the server answered.
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { ErrorInfoSchema } from "@gen/google/rpc/error_details_pb";
import { TransitionOutcome, type Item } from "@gen/kernel/v1/kernel_pb";
import { describe, expect, test, vi } from "vitest";
import type { Api } from "../app/api";
import { NS, item, itemName, typeName } from "../test/fixtures";
import {
  balanceOf,
  balanceSearch,
  balanceWarning,
  blanks,
  byRequester,
  decide,
  denialSentence,
  leaveFields,
  othersInPeriod,
  overlapSearch,
  pendingDays,
  pendingSentence,
  refusalSentence,
  requestStatus,
  requesterOf,
  sendRequest,
  suggestDays,
  unpaidNote,
  validateDecision,
  weekdays,
  type LeaveForm,
} from "./leave";
import { refusalOf, type Refusal } from "./refusal";

const vr = typeName("vacation_request", 3);
const lb = typeName("leave_balance", 1);
const request = (id: string, opts: { state?: string; creator?: string; onBehalfOf?: string; start?: string; end?: string; days?: string } = {}) =>
  item(id, vr, {
    state: opts.state ?? "submitted",
    creator: opts.creator ?? "user:ana",
    onBehalfOf: opts.onBehalfOf,
    fields: { f_start: opts.start ?? "2099-07-01", f_end: opts.end ?? "2099-07-05", f_days: opts.days ?? "5", f_leave_type: "annual" },
  });
const ids = (items: Item[]) => items.map((i) => i.name.split("/").pop());
const refusal = (reason: string, metadata: Record<string, string> = {}, code = "FAILED_PRECONDITION"): Refusal => ({
  code,
  reason,
  message: "",
  metadata,
  violations: [],
});

describe("the balance", () => {
  test("the search: open balances of the caller", () => {
    expect(balanceSearch(NS)).toEqual({
      parent: NS,
      filter: {
        types: [`${NS}/types/leave_balance`],
        states: ["open"],
        fields: [{ field: "f_owner", eq: { kind: { case: "stringValue", value: "me" } } }],
      },
      pageSize: 10,
    });
  });

  test("in numbers; an unset used is 0", () => {
    const b = item("b1", lb, { state: "open", fields: { f_owner: "user:ana", f_allowance: "25", f_used: "10" } });
    expect(balanceOf(b)).toEqual({ name: itemName("b1"), owner: "user:ana", allowance: 25, used: 10, left: 15 });
    expect(balanceOf(item("b2", lb, { fields: { f_owner: "user:ana", f_allowance: "10" } }))).toMatchObject({ used: 0, left: 10 });
  });

  test("days waiting for approval: the submitted requests' days", () => {
    const rs = [request("a", { days: "5" }), request("b", { state: "draft", days: "3" }), request("c", { days: "2" }), request("d", { state: "approved", days: "4" })];
    expect(pendingDays(rs)).toBe(7);
  });

  test("a warning when the days asked are more than the days left (the app refuses nothing)", () => {
    const b = { name: itemName("b1"), owner: "user:ana", allowance: 10, used: 5, left: 5 };
    expect(balanceWarning(b, 5)).toBeUndefined();
    expect(balanceWarning(b, 8)).toBe(
      "That's more than the 5 days you have left. Your approver won't be able to approve it unless HR adds days.",
    );
    expect(balanceWarning({ ...b, left: 1 }, 2)).toBe(
      "That's more than the 1 day you have left. Your approver won't be able to approve it unless HR adds days.",
    );
    expect(balanceWarning(undefined, 2)).toBe("HR hasn't opened a leave balance for you yet, so this will be refused when you send it.");
  });

  test("unpaid leave counts too: the process takes it from the balance", () => {
    expect(unpaidNote("unpaid")).toBe("Unpaid leave is counted against your balance too.");
    expect(unpaidNote("annual")).toBeUndefined();
  });
});

describe("whose request", () => {
  test("the requester is whom it was created for, else its creator", () => {
    expect(requesterOf(request("a"))).toBe("user:ana");
    expect(requesterOf(request("b", { creator: "agent:triage-bot", onBehalfOf: "user:ana" }))).toBe("user:ana");
  });

  test("byRequester keeps one person's requests, in the order given", () => {
    const rs = [request("a"), request("b", { creator: "user:bob" }), request("c", { creator: "agent:triage-bot", onBehalfOf: "user:ana" })];
    expect(ids(byRequester(rs, "user:ana"))).toEqual(["a", "c"]);
  });

  test("the period search: requests whose dates overlap the period", () => {
    expect(overlapSearch(NS, "2099-07-01", "2099-07-05")).toEqual({
      parent: NS,
      filter: {
        types: [`${NS}/types/vacation_request`],
        fields: [
          { field: "f_start", lte: { kind: { case: "stringValue", value: "2099-07-05" } } },
          { field: "f_end", gte: { kind: { case: "stringValue", value: "2099-07-01" } } },
        ],
      },
      pageSize: 500,
    });
  });

  test("their other requests in the same period: same requester, not this one, not drafts", () => {
    const self = request("self");
    const found = [self, request("other", { state: "approved" }), request("bob", { creator: "user:bob" }), request("draft", { state: "draft" }), request("gone", { state: "withdrawn" })];
    expect(ids(othersInPeriod(found, self))).toEqual(["other", "gone"]);
  });
});

describe("a request's status, in the employee's words", () => {
  test.each([
    ["draft", undefined, "Draft"],
    ["submitted", undefined, "Waiting for bob"],
    ["approved", undefined, "Approved"],
    ["rejected", undefined, "Rejected"],
    ["withdrawn", undefined, "Withdrawn"],
    ["expired", undefined, "Expired"],
    ["submitted", "approved", "Being approved"],
    ["draft", "submitted", "Checking with HR"],
  ])("%s, hold toward %s: %s", (state, held, want) => {
    const it = item("r", vr, {
      state,
      fields: { f_approver: "user:bob" },
      hold: held ? { check: "x", fromState: state, requestedState: held } : undefined,
    });
    expect(requestStatus(it)).toBe(want);
  });

  test("submitted before HR named an approver", () => {
    expect(requestStatus(item("r", vr, { state: "submitted" }))).toBe("Waiting for approval");
  });
});

describe("the form", () => {
  const form: LeaveForm = { start: "2099-07-01", end: "2099-07-05", days: "3", leaveType: "annual", reason: "  Beach  " };

  test("days suggested: the weekdays from the first day to the last, both included", () => {
    expect(weekdays("2026-11-23", "2026-11-27")).toBe(5);
    expect(weekdays("2026-11-21", "2026-11-22")).toBe(0);
    expect(weekdays("2026-11-20", "2026-11-23")).toBe(2);
    expect(weekdays("2026-11-23", "2026-12-04")).toBe(10);
    expect(weekdays("2026-11-27", "2026-11-23")).toBe(0);
    expect(weekdays("", "2026-11-23")).toBe(0);
  });

  test("the days field follows the dates while the person has not typed in it", () => {
    expect(suggestDays("2026-11-23", "2026-11-27", "", false)).toEqual({ days: "5" });
    expect(suggestDays("2026-11-23", "2026-11-25", "5", false)).toEqual({ days: "3" });
    expect(suggestDays("2026-11-21", "2026-11-22", "3", false)).toEqual({ days: "0" });
    expect(suggestDays("2026-11-27", "2026-11-23", "3", false)).toEqual({ days: "" });
    expect(suggestDays("", "2026-11-23", "3", false)).toEqual({ days: "" });
  });

  test("once typed in, a date change leaves the days and shows the suggestion as a hint", () => {
    expect(suggestDays("2026-11-23", "2026-11-27", "3", true)).toEqual({ days: "3", hint: "5 weekdays" });
    expect(suggestDays("2026-11-23", "2026-11-23", "3", true)).toEqual({ days: "3", hint: "1 weekday" });
    expect(suggestDays("2026-11-21", "2026-11-22", "1", true)).toEqual({ days: "1", hint: "0 weekdays" });
    expect(suggestDays("2026-11-27", "2026-11-23", "3", true)).toEqual({ days: "3" });
  });

  test("only blanks are checked before sending; the dates' sense is the server's", () => {
    expect(blanks({ start: "", end: "", days: " ", leaveType: "", reason: "" })).toEqual({
      start: "Choose the first day.",
      end: "Choose the last day.",
      days: "Say how many days.",
      leaveType: "Choose a leave type.",
    });
    expect(blanks({ ...form, start: "2099-07-09", days: "0" })).toEqual({});
  });

  test("the fields sent; the reason only when given", () => {
    expect(leaveFields(form)).toEqual({ f_start: "2099-07-01", f_end: "2099-07-05", f_days: "3", f_leave_type: "annual", f_comment: "Beach" });
    expect(leaveFields({ ...form, reason: " " })).not.toHaveProperty("f_comment");
  });
});

// A fake API: createItem makes the draft; transitionItem answers with the
// outcome given, or throws the error given.
function fakeApi(answer: { outcome?: TransitionOutcome; reason?: string; error?: unknown; createError?: unknown; commentError?: unknown } = {}) {
  type Req = Record<string, any>;
  const createItem = vi.fn(async (req: Req) => {
    if (req.type === typeName("comment", 1)) {
      if (answer.commentError) throw answer.commentError;
      return { item: item("c1", req.type, { container: req.container }), itemSeq: 1n, commitSeq: 8n };
    }
    if (answer.createError) throw answer.createError;
    return { item: item("draft-1", req.type, { state: "draft", fields: req.fields }), itemSeq: 1n, commitSeq: 5n };
  });
  const transitionItem = vi.fn(async (req: Req) => {
    if (answer.error) throw answer.error;
    const outcome = answer.outcome ?? TransitionOutcome.APPLIED;
    const state = outcome === TransitionOutcome.APPLIED ? req.toState : "draft";
    return { item: item(req.name.split("/").pop(), vr, { state }), itemSeq: 2n, commitSeq: 6n, outcome, reason: answer.reason ?? "", hold: "" };
  });
  return { api: { createItem, transitionItem } as unknown as Pick<Api, "createItem" | "transitionItem">, createItem, transitionItem };
}

describe("sendRequest: a draft, then the move to submitted", () => {
  const form: LeaveForm = { start: "2099-07-01", end: "2099-07-05", days: "3", leaveType: "annual", reason: "Beach" };

  test("first time: creates the draft with the fields, then asks to submit it, waiting up to 5 s for HR's check", async () => {
    const { api, createItem, transitionItem } = fakeApi();
    const out = await sendRequest(api, NS, form);
    expect(createItem).toHaveBeenCalledTimes(1);
    expect(createItem.mock.calls[0]![0]).toMatchObject({ parent: NS, type: vr, fields: leaveFields(form) });
    expect(createItem.mock.calls[0]![0].requestId).toEqual(expect.any(String));
    const asked = transitionItem.mock.calls[0]![0];
    expect(asked).toMatchObject({ name: itemName("draft-1"), toState: "submitted", wait: { seconds: 5n } });
    expect(asked).not.toHaveProperty("fields");
    expect(asked).not.toHaveProperty("etag");
    expect(out).toMatchObject({ kind: "sent", item: { name: itemName("draft-1"), state: "submitted" }, commitSeq: 6n });
  });

  test("again, after a refusal: the same draft, the form's fields carried by the move; a cleared reason is unset", async () => {
    const { api, createItem, transitionItem } = fakeApi();
    const draft = item("draft-1", vr, { state: "draft" });
    await sendRequest(api, NS, { ...form, reason: "" }, draft);
    expect(createItem).not.toHaveBeenCalled();
    expect(transitionItem.mock.calls[0]![0]).toMatchObject({
      name: draft.name,
      toState: "submitted",
      fields: { f_start: "2099-07-01", f_end: "2099-07-05", f_days: "3", f_leave_type: "annual", f_comment: null },
      wait: { seconds: 5n },
    });
  });

  test("HR still checking: pending", async () => {
    const { api } = fakeApi({ outcome: TransitionOutcome.PENDING });
    expect(await sendRequest(api, NS, form)).toMatchObject({ kind: "pending", item: { name: itemName("draft-1") } });
  });

  test("denied by the check (no balance): the reason, the draft kept", async () => {
    const { api } = fakeApi({ outcome: TransitionOutcome.DENIED, reason: "NO_BALANCE" });
    expect(await sendRequest(api, NS, form)).toMatchObject({ kind: "denied", reason: "NO_BALANCE", item: { name: itemName("draft-1"), state: "draft" } });
  });

  test("the check failed: failed", async () => {
    const { api } = fakeApi({ outcome: TransitionOutcome.ERROR });
    expect(await sendRequest(api, NS, form)).toMatchObject({ kind: "failed", item: { name: itemName("draft-1") } });
  });

  test("refused by a rule (the dates): the error, and the draft to send again", async () => {
    const err = new ConnectError("guard", Code.FailedPrecondition);
    const { api } = fakeApi({ error: err });
    const out = await sendRequest(api, NS, form);
    expect(out).toMatchObject({ kind: "refused", draft: { name: itemName("draft-1") } });
    expect(out.kind === "refused" && out.error).toBe(err);
  });

  test("a create that fails throws: there is no draft", async () => {
    const { api, transitionItem } = fakeApi({ createError: new ConnectError("down", Code.Unavailable) });
    await expect(sendRequest(api, NS, form)).rejects.toThrow("down");
    expect(transitionItem).not.toHaveBeenCalled();
  });
});

describe("decide: the approver's move, then the comment", () => {
  const req = item("r1", vr, { state: "submitted", fields: { f_approver: "user:bob" } });

  test("a reject needs a comment; an approve does not", () => {
    expect(validateDecision("rejected", "  ")).toEqual({ comment: "Say why you're rejecting it." });
    expect(validateDecision("rejected", "Team offsite that week")).toEqual({});
    expect(validateDecision("approved", "")).toEqual({});
  });

  test("approved: the move carries the etag the approver saw and waits up to 5 s for the deduction; then the comment inside the request", async () => {
    const { api, createItem, transitionItem } = fakeApi();
    const out = await decide(api, NS, req, "approved", "  Enjoy  ");
    const asked = transitionItem.mock.calls[0]![0];
    expect(asked).toMatchObject({ name: req.name, toState: "approved", etag: req.etag, wait: { seconds: 5n } });
    expect(asked).not.toHaveProperty("fields");
    expect(createItem).toHaveBeenCalledTimes(1);
    expect(createItem.mock.calls[0]![0]).toMatchObject({ parent: NS, type: typeName("comment", 1), container: req.name, fields: { f_body: "Enjoy" } });
    expect(out).toMatchObject({ kind: "applied", commentSaved: true, item: { state: "approved" } });
  });

  test("no comment given: none written", async () => {
    const { api, createItem } = fakeApi();
    expect(await decide(api, NS, req, "approved", "")).toMatchObject({ kind: "applied", commentSaved: true });
    expect(createItem).not.toHaveBeenCalled();
  });

  test("refused (not enough days): no comment is written", async () => {
    const { api, createItem } = fakeApi({ outcome: TransitionOutcome.DENIED, reason: "INSUFFICIENT_BALANCE" });
    expect(await decide(api, NS, req, "approved", "Enjoy")).toMatchObject({ kind: "denied", reason: "INSUFFICIENT_BALANCE" });
    expect(createItem).not.toHaveBeenCalled();
  });

  test("still checking: pending, no comment yet", async () => {
    const { api, createItem } = fakeApi({ outcome: TransitionOutcome.PENDING });
    expect(await decide(api, NS, req, "approved", "Enjoy")).toMatchObject({ kind: "pending" });
    expect(createItem).not.toHaveBeenCalled();
  });

  test("the decision stands when its comment fails", async () => {
    const { api } = fakeApi({ commentError: new Error("down") });
    expect(await decide(api, NS, req, "rejected", "Offsite")).toMatchObject({ kind: "applied", commentSaved: false, item: { state: "rejected" } });
  });

  test("the request changed since the approver opened it (recalled, edited, sent again): STALE, nothing applied, no comment", async () => {
    const stale = new ConnectError("stale", Code.Aborted, undefined, [
      { desc: ErrorInfoSchema, value: create(ErrorInfoSchema, { reason: "STALE", domain: "pli.francoisnyc.github.io" }) },
    ]);
    const createItem = vi.fn();
    // The request as it is now: ana recalled it, changed the dates and sent it again.
    const now = { ...req, etag: "c4" };
    const transitionItem = vi.fn(async (r: Record<string, any>) => {
      if (r.etag !== now.etag) throw stale;
      return { item: now, itemSeq: 5n, commitSeq: 9n, outcome: TransitionOutcome.APPLIED, reason: "", hold: "" };
    });
    const api = { createItem, transitionItem } as unknown as Pick<Api, "createItem" | "transitionItem">;
    const err = await decide(api, NS, req, "approved", "Enjoy").catch((e: unknown) => e);
    expect(err).toBe(stale);
    expect(createItem).not.toHaveBeenCalled();
    expect(refusalSentence(refusalOf(err), { viewer: "user:bob", requester: "user:ana" })).toBe(
      "This request changed since you opened it. It has been reloaded: check it, then decide again.",
    );
  });

  test("a refusal by a rule is thrown, as every other write's", async () => {
    const { api, createItem } = fakeApi({ error: new ConnectError("not allowed", Code.PermissionDenied) });
    await expect(decide(api, NS, req, "approved", "x")).rejects.toThrow("not allowed");
    expect(createItem).not.toHaveBeenCalled();
  });
});

describe("refusals and denials in plain words", () => {
  const ana = { viewer: "user:ana", requester: "user:ana" };
  const bob = { viewer: "user:bob", requester: "user:ana" };

  test("what the HR check and the balance answer, to the requester", () => {
    expect(denialSentence("NO_BALANCE", ana)).toBe("HR hasn't opened a leave balance for you yet. Ask HR to open one, then send it again.");
    expect(denialSentence("no_balance", ana)).toBe("HR hasn't opened a leave balance for you yet. Ask HR to open one, then send it again.");
    expect(denialSentence("AMBIGUOUS_BALANCE", ana)).toBe("You have more than one open leave balance. Ask HR to close the old one, then send it again.");
    expect(denialSentence("NO_ELIGIBLE_APPROVER", ana)).toBe("Nobody can approve your request: the HR directory names you as your own manager. Ask HR.");
    expect(denialSentence("UNKNOWN_EMPLOYEE", ana)).toBe("The HR directory doesn't know you, so nobody can approve your request. Ask HR.");
    expect(denialSentence("INSUFFICIENT_BALANCE", { ...ana, asked: 8, left: 5 })).toBe("You don't have enough days left: this asks for 8 and you have 5.");
    expect(denialSentence("INSUFFICIENT_BALANCE", ana)).toBe("You don't have enough days left for this.");
  });

  test("to the approver, about the requester", () => {
    expect(denialSentence("INSUFFICIENT_BALANCE", { ...bob, asked: 8, left: 5 })).toBe(
      "ana doesn't have enough days left: this asks for 8 and ana has 5. Reject it, or ask HR to add days.",
    );
    expect(denialSentence("INSUFFICIENT_BALANCE", bob)).toBe("ana doesn't have enough days left for this. Reject it, or ask HR to add days.");
    expect(denialSentence("NO_BALANCE", bob)).toBe("ana has no open leave balance.");
  });

  test("a reason the app does not know is shown as it came", () => {
    expect(denialSentence("LINK_TARGET_CHANGED", ana)).toBe("The system refused this (link_target_changed).");
  });

  test("still checking, by the move asked for", () => {
    expect(pendingSentence("submitted")).toBe("Still checking with HR. This page will show the answer.");
    expect(pendingSentence("approved")).toBe("Still checking the balance. This page will show the answer.");
    expect(pendingSentence("closed")).toBe("Still being checked. This page will show the answer.");
  });

  describe("the dates that make no sense: the server refuses, the app says which part from what it sent", () => {
    const sent: LeaveForm = { start: "2099-07-05", end: "2099-07-01", days: "3", leaveType: "annual", reason: "" };
    const ctx = (s: LeaveForm) => ({ ...ana, sent: s, today: "2026-11-21" });
    test.each([
      [sent, "The last day is before the first day."],
      [{ ...sent, start: "2026-11-20", end: "2026-11-27" }, "The first day has already passed."],
      [{ ...sent, end: "2099-07-09", days: "0" }, "Ask for at least one day."],
      [{ ...sent, end: "2099-07-09" }, "The dates and days don't make sense: the last day must be on or after the first, the first can't have passed, and you need at least one day."],
    ])("%j", (s, want) => {
      expect(refusalSentence(refusal("GUARD_FAILED"), ctx(s))).toBe(want);
    });

    test("without what was sent, the whole rule", () => {
      expect(refusalSentence(refusal("GUARD_FAILED"), ana)).toBe(
        "The dates and days don't make sense: the last day must be on or after the first, the first can't have passed, and you need at least one day.",
      );
    });
  });

  test("missing fields, in the form's words", () => {
    expect(refusalSentence(refusal("MISSING_FIELDS", { fields: "f_start,f_leave_type" }), ana)).toBe("Fill in the first day and the leave type first.");
  });

  test("a frozen request: recall it first", () => {
    expect(refusalSentence(refusal("FIELD_FROZEN", { fields: "f_days", state: "submitted" }), ana)).toBe(
      "This can't be changed while it waits for approval. Recall it first.",
    );
  });

  test("no single balance on a direct move: the link's reason", () => {
    expect(refusalSentence(refusal("LINK_UNRESOLVED", { link_type: "balance", reason: "no_balance" }), ana)).toBe(
      "HR hasn't opened a leave balance for you yet. Ask HR to open one, then send it again.",
    );
  });

  test("anything else: the apps' usual sentence", () => {
    expect(refusalSentence(refusal("NOT_ALLOWED", {}, "PERMISSION_DENIED"), bob)).toBe("You don't have permission to do that.");
  });
});
