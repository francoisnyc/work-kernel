// A case's timeline: who did what when, from the request's history, its
// decision comments, and what its reactions wrote on the balance. System
// steps are shown, marked as the system's; a move the kernel finished
// (after a check or a deduction) is said as the person's who asked.
import type { ItemEvent } from "@gen/kernel/v1/kernel_pb";
import { expect, test } from "vitest";
import { Kind, event, item, itemName, typeName, vacationType } from "../test/fixtures";
import { caseTimeline, type CaseContext } from "./casetimeline";

const at = (min: number) => new Date(Date.UTC(2026, 10, 21, 9, min)).toISOString();
const req = "r1-0000";
const bal = "b1-0000";
const hr = "hook:vacation_request:hr_check";
const deduct = "reaction:vacation_request:deduct";
const refund = "reaction:vacation_request:refund";

// ana's request: sent (HR names bob, the balance is linked), reminded,
// refused once for lack of days, approved, withdrawn and refunded.
const history: ItemEvent[] = [
  event(Kind.CREATED, at(0), { actor: "user:ana", toState: "draft", seq: 1 }),
  event(Kind.TRANSITION_REQUESTED, at(1), { actor: "user:ana", requestedState: "submitted", hold: "h1", seq: 2 }),
  event(Kind.TRANSITIONED, at(2), {
    actor: hr,
    onBehalfOf: "user:ana",
    fromState: "draft",
    toState: "submitted",
    fields: { f_approver: "user:bob" },
    linkType: "balance",
    linkTarget: itemName(bal),
    hold: "h1",
    seq: 3,
  }),
  event(Kind.HOOK_RAN, at(3), { actor: "hook:vacation_request:remind_approver", hook: "remind_approver", timer: "remind", run: 1, seq: 4 }),
  event(Kind.TRANSITION_REQUESTED, at(4), { actor: "user:bob", requestedState: "approved", hold: "h2", seq: 5 }),
  event(Kind.TRANSITION_DENIED, at(5), { actor: deduct, onBehalfOf: "user:bob", requestedState: "approved", reason: "insufficient_balance", hold: "h2", seq: 6 }),
  event(Kind.TRANSITION_REQUESTED, at(6), { actor: "user:bob", requestedState: "approved", hold: "h3", seq: 7 }),
  event(Kind.TRANSITIONED, at(8), { actor: deduct, onBehalfOf: "user:bob", fromState: "submitted", toState: "approved", hold: "h3", seq: 8 }),
  event(Kind.HOOK_RAN, at(10), { actor: "hook:vacation_request:notify_approved", hook: "notify_approved", seq: 9 }),
  event(Kind.TRANSITIONED, at(11), { actor: "user:ana", fromState: "approved", toState: "withdrawn", seq: 10 }),
  event(Kind.REACTION_APPLIED, at(13), { actor: refund, reaction: "vacation_request:refund", seq: 11 }),
  event(Kind.HOOK_RAN, at(14), { actor: "hook:vacation_request:refund_email", hook: "refund_email", seq: 12 }),
];

// The balance's history: HR opened it; this request's deduction and
// refund; another request's deduction, which is not this case's.
const balanceEvents: ItemEvent[] = [
  event(Kind.CREATED, at(0), { actor: "user:hana", toState: "open", fields: { f_owner: "user:ana", f_allowance: "25" }, seq: 1 }),
  event(Kind.UPDATED, at(7), { actor: deduct, causationId: `${req}:8`, added: { f_used: "5" }, fields: { f_used: "10" }, seq: 2 }),
  event(Kind.UPDATED, at(9), { actor: deduct, causationId: "other-0000:4", added: { f_used: "2" }, fields: { f_used: "12" }, seq: 3 }),
  event(Kind.UPDATED, at(12), { actor: refund, causationId: `${req}:10`, added: { f_used: "-5" }, fields: { f_used: "7" }, seq: 4 }),
];

const comments = [item("c1", typeName("comment", 1), { container: itemName(req), creator: "user:bob", at: at(9), fields: { f_body: "Enjoy the beach" } })];

const ctx = (viewer: string, extra: Partial<CaseContext> = {}): CaseContext => ({
  type: vacationType,
  request: itemName(req),
  requester: "user:ana",
  viewer,
  balanceEvents,
  comments,
  ...extra,
});
const lines = (events: ItemEvent[], c: CaseContext) => caseTimeline(events, c).map((e) => `${e.who}: ${e.text}${e.system ? " [system]" : ""}${e.refused ? " [refused]" : ""}`);

test("the whole case, as the requester sees it, oldest first", () => {
  expect(lines(history, ctx("user:ana"))).toEqual([
    "You: created the request",
    "You: sent it for approval",
    "System: The HR check named bob as approver [system]",
    "System: Linked to your leave balance [system]",
    "System: Reminder sent to the approver (1 of 2) [system]",
    "bob: tried to approve it: not enough days left [refused]",
    "System: 5 days taken from your balance (10 used) [system]",
    "bob: approved it",
    'bob: commented: "Enjoy the beach"',
    "System: Email sent to you: approved [system]",
    "You: withdrew it",
    "System: 5 days given back to your balance (7 used) [system]",
    "System: Email sent to you: days given back [system]",
  ]);
});

test("as the approver sees it", () => {
  const got = lines(history, ctx("user:bob"));
  expect(got.slice(0, 4)).toEqual([
    "ana: created the request",
    "ana: sent it for approval",
    "System: The HR check named you as approver [system]",
    "System: Linked to ana's leave balance [system]",
  ]);
  expect(got).toContain("You: tried to approve it: not enough days left [refused]");
  expect(got).toContain("System: 5 days taken from ana's balance (10 used) [system]");
  expect(got).toContain("You: approved it");
  expect(got).toContain('You: commented: "Enjoy the beach"');
  expect(got).toContain("System: Email sent to ana: approved [system]");
});

test("entries carry their time", () => {
  expect(caseTimeline(history, ctx("user:ana"))[0]).toEqual({ at: at(0), who: "You", text: "created the request", system: false, refused: false });
});

test("without the balance's history (hidden from the viewer): no amounts; the refund said from the request's own record", () => {
  const got = lines(history, ctx("user:dana", { balanceEvents: undefined }));
  expect(got.some((l) => l.includes("days taken"))).toBe(false);
  expect(got).toContain("System: Days given back to the balance [system]");
  expect(got).toContain("bob: approved it");
});

test("an approval still being checked: the ask, said as open", () => {
  const open = [history[0]!, history[1]!, history[2]!, event(Kind.TRANSITION_REQUESTED, at(4), { actor: "user:bob", requestedState: "approved", hold: "h9", seq: 5 })];
  expect(lines(open, ctx("user:ana")).at(-1)).toBe("bob: asked to approve it; the system is checking");
});

test("the HR check's refusals, by their reason", () => {
  const denied = (reason: string) => [
    event(Kind.TRANSITION_REQUESTED, at(1), { actor: "user:dana", requestedState: "submitted", hold: "h1", seq: 2 }),
    event(Kind.TRANSITION_DENIED, at(2), { actor: hr, onBehalfOf: "user:dana", requestedState: "submitted", reason, hold: "h1", seq: 3 }),
  ];
  const c = ctx("user:dana", { requester: "user:dana" });
  expect(lines(denied("no_eligible_approver"), c)).toEqual(["You: tried to send it for approval: no one can approve it [refused]"]);
  expect(lines(denied("unknown_employee"), c)).toEqual(["You: tried to send it for approval: not in the HR directory [refused]"]);
  expect(lines(denied("no_balance"), c)).toEqual(["You: tried to send it for approval: no open leave balance [refused]"]);
  expect(lines(denied("ambiguous_balance"), c)).toEqual(["You: tried to send it for approval: more than one open leave balance [refused]"]);
  expect(lines(denied("budget_exhausted"), c)).toEqual(["You: tried to send it for approval: budget exhausted [refused]"]);
});

test("a check that failed", () => {
  const failed = [
    event(Kind.TRANSITION_REQUESTED, at(1), { actor: "user:ana", requestedState: "submitted", hold: "h1", seq: 2 }),
    event(Kind.TRANSITION_FAILED, at(2), { actor: hr, onBehalfOf: "user:ana", requestedState: "submitted", reason: "budget_exhausted", hold: "h1", seq: 3 }),
  ];
  expect(lines(failed, ctx("user:ana"))).toEqual(["You: tried to send it for approval: the check failed [refused]"]);
});

test("expiry, reminders, a recall, a rejection and an edit", () => {
  const evs = [
    event(Kind.TRANSITIONED, at(1), { actor: "user:ana", fromState: "submitted", toState: "draft", seq: 2 }),
    event(Kind.UPDATED, at(2), { actor: "user:ana", fields: { f_comment: "Beach, then mountains" }, seq: 3 }),
    event(Kind.UPDATED, at(3), { actor: "user:ana", fields: { f_days: "3", f_end: "2099-07-03" }, seq: 4 }),
    event(Kind.HOOK_RAN, at(4), { actor: "hook:vacation_request:remind_approver", hook: "remind_approver", timer: "remind", run: 2, seq: 5 }),
    event(Kind.TRANSITIONED, at(5), { actor: "system:timer", fromState: "submitted", toState: "expired", seq: 6 }),
    event(Kind.HOOK_RAN, at(6), { actor: "hook:vacation_request:notify_expired", hook: "notify_expired", seq: 7 }),
    event(Kind.TRANSITIONED, at(7), { actor: "user:bob", fromState: "submitted", toState: "rejected", seq: 8 }),
    event(Kind.HOOK_RAN, at(8), { actor: "hook:vacation_request:notify_rejected", hook: "notify_rejected", seq: 9 }),
    event(Kind.HOOK_FAILED, at(9), { actor: "hook:vacation_request:notify_rejected", hook: "notify_rejected", reason: "action_failed", seq: 10 }),
    event(Kind.HOOK_RAN, at(10), { actor: "hook:vacation_request:new_hook", hook: "new_hook", seq: 11 }),
  ];
  expect(lines(evs, ctx("user:ana", { comments: [] }))).toEqual([
    "You: recalled it",
    "You: changed the reason",
    "You: changed the number of days and the last day",
    "System: Reminder sent to the approver (2 of 2) [system]",
    "System: Expired: nobody decided within 5 days [system]",
    "System: Email sent to you: expired [system]",
    "bob: rejected it",
    "System: Email sent to you: rejected [system]",
    "System: Couldn't send the email to you: rejected [system]",
    "System: Ran new hook [system]",
  ]);
});
