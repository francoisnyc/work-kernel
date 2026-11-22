// The Requests area on the vacation request (vacation_request@3) and the
// leave balance (leave_balance@1): a balance in numbers, a person's
// requests, the form, sending it, the approver's decision, and every
// refusal in plain words.
//
// The app judges no guard and no invariant. It warns before sending (more
// days than are left) and explains what the server answered; the server
// alone refuses. The balance is checked only when an approval deducts it.
import type { JsonObject, MessageInitShape } from "@bufbuild/protobuf";
import { TransitionOutcome, type Item, type SearchItemsRequestSchema } from "@gen/kernel/v1/kernel_pb";
import type { Api } from "../app/api";
import { personName } from "./people";
import { explain, type Refusal } from "./refusal";

// The version new requests are made with: the latest, with its reminders,
// expiry and emails.
export const REQUEST_VERSION = 3;

const str = (value: string) => ({ kind: { case: "stringValue" as const, value } });
const int = (v: unknown): number => {
  const n = typeof v === "string" || typeof v === "number" ? Number(v) : NaN;
  return Number.isFinite(n) ? n : 0;
};
const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

// --- The balance ---

export type Balance = { name: string; owner: string; allowance: number; used: number; left: number };

// The caller's open balances (one, normally; several is HR's mistake).
export function balanceSearch(ns: string): MessageInitShape<typeof SearchItemsRequestSchema> {
  return {
    parent: ns,
    filter: { types: [`${ns}/types/leave_balance`], states: ["open"], fields: [{ field: "f_owner", eq: str("me") }] },
    pageSize: 10,
  };
}

// Every open balance the caller can read: HR's view.
export function allBalancesSearch(ns: string, state: "open" | "closed" = "open"): MessageInitShape<typeof SearchItemsRequestSchema> {
  return { parent: ns, filter: { types: [`${ns}/types/leave_balance`], states: [state] }, pageSize: 500, orderBy: "f_owner" };
}

export function balanceOf(item: Item): Balance {
  const allowance = int(item.fields?.f_allowance);
  const used = int(item.fields?.f_used);
  return { name: item.name, owner: String(item.fields?.f_owner ?? ""), allowance, used, left: allowance - used };
}

// The days of the requests waiting for approval.
export function pendingDays(requests: Item[]): number {
  return requests.filter((r) => r.state === "submitted").reduce((sum, r) => sum + int(r.fields?.f_days), 0);
}

// Before sending: more days than are left. A warning, never a refusal.
export function balanceWarning(balance: Balance | undefined, days: number): string | undefined {
  if (!balance) return "HR hasn't opened a leave balance for you yet, so this will be refused when you send it.";
  if (days <= balance.left) return undefined;
  return `That's more than the ${plural(balance.left, "day")} you have left. Your approver won't be able to approve it unless HR adds days.`;
}

// The deduction has no condition on the leave type (a gap of the Type).
export function unpaidNote(leaveType: string): string | undefined {
  return leaveType === "unpaid" ? "Unpaid leave is counted against your balance too." : undefined;
}

// --- Whose request ---

// The person a request is for: whom it was created for, else its creator.
export function requesterOf(item: Item): string {
  return item.creatorOnBehalfOf || item.creator;
}

// There is no filter on the creator: a person's requests are picked out
// of a search here.
export function byRequester(items: Item[], principal: string): Item[] {
  return items.filter((i) => requesterOf(i) === principal);
}

// Every vacation request, newest first: "My requests" keeps the caller's.
export function requestsSearch(ns: string, pageToken = ""): MessageInitShape<typeof SearchItemsRequestSchema> {
  return { parent: ns, filter: { types: [`${ns}/types/vacation_request`] }, pageSize: 500, pageToken };
}

// Requests whose dates overlap [start, end].
export function overlapSearch(ns: string, start: string, end: string): MessageInitShape<typeof SearchItemsRequestSchema> {
  return {
    parent: ns,
    filter: {
      types: [`${ns}/types/vacation_request`],
      fields: [
        { field: "f_start", lte: str(end) },
        { field: "f_end", gte: str(start) },
      ],
    },
    pageSize: 500,
  };
}

// The requester's other requests among those found: not this one, not a
// draft (not asked for yet).
export function othersInPeriod(found: Item[], request: Item): Item[] {
  const who = requesterOf(request);
  return found.filter((i) => i.name !== request.name && i.state !== "draft" && requesterOf(i) === who);
}

// A request's status in the employee's words.
export function requestStatus(item: Item): string {
  if (item.hold) {
    if (item.hold.requestedState === "submitted") return "Checking with HR";
    if (item.hold.requestedState === "approved") return "Being approved";
    return "Being checked";
  }
  switch (item.state) {
    case "draft":
      return "Draft";
    case "submitted":
      return typeof item.fields?.f_approver === "string" ? `Waiting for ${personName(item.fields.f_approver)}` : "Waiting for approval";
    default:
      return item.state.charAt(0).toUpperCase() + item.state.slice(1).replace(/_/g, " ");
  }
}

// --- The form ---

export type LeaveForm = { start: string; end: string; days: string; leaveType: string; reason: string };

const date = (d: string) => (/^\d{4}-\d{2}-\d{2}$/.test(d) ? Date.parse(`${d}T00:00:00Z`) : NaN);

// The weekdays (Monday to Friday) from start to end, both included; 0 when
// the dates are missing or the wrong way round. No holiday calendar.
export function weekdays(start: string, end: string): number {
  const a = date(start);
  const b = date(end);
  if (Number.isNaN(a) || Number.isNaN(b) || b < a) return 0;
  let n = 0;
  for (let t = a; t <= b; t += 86_400_000) {
    const day = new Date(t).getUTCDay();
    if (day !== 0 && day !== 6) n++;
  }
  return n;
}

// The days field after a date change: the weekday count while the person
// has not typed in it; once they have, their number stays and the count
// is a hint. Dates that make no range suggest nothing.
export function suggestDays(start: string, end: string, current: string, typed: boolean): { days: string; hint?: string } {
  const a = date(start);
  const b = date(end);
  const range = !Number.isNaN(a) && !Number.isNaN(b) && b >= a;
  if (!typed) return { days: range ? String(weekdays(start, end)) : "" };
  return range ? { days: current, hint: plural(weekdays(start, end), "weekday") } : { days: current };
}

// The form's own check before sending: blanks only. Whether the dates
// make sense is the server's to say (the Type's guard).
export function blanks(form: LeaveForm): Partial<Record<keyof LeaveForm, string>> {
  const out: Partial<Record<keyof LeaveForm, string>> = {};
  if (form.start.trim() === "") out.start = "Choose the first day.";
  if (form.end.trim() === "") out.end = "Choose the last day.";
  if (form.days.trim() === "") out.days = "Say how many days.";
  if (form.leaveType.trim() === "") out.leaveType = "Choose a leave type.";
  return out;
}

export function leaveFields(form: LeaveForm): Record<string, string> {
  const out: Record<string, string> = {
    f_start: form.start,
    f_end: form.end,
    f_days: form.days.trim(),
    f_leave_type: form.leaveType,
  };
  if (form.reason.trim() !== "") out.f_comment = form.reason.trim();
  return out;
}

// --- Sending ---

type Answer = { item?: Item; commitSeq: bigint; outcome: TransitionOutcome; reason: string };

export type SendResult =
  | { kind: "sent"; item: Item; commitSeq: bigint }
  | { kind: "pending"; item: Item; commitSeq: bigint }
  | { kind: "denied"; item: Item; reason: string; commitSeq: bigint }
  | { kind: "failed"; item: Item; commitSeq: bigint }
  | { kind: "refused"; draft: Item; error: unknown };

const WAIT = { seconds: 5n };

function outcomeOf(res: Answer, fallback: Item) {
  const item = res.item ?? fallback;
  switch (res.outcome) {
    case TransitionOutcome.APPLIED:
      return { kind: "sent" as const, item, commitSeq: res.commitSeq };
    case TransitionOutcome.PENDING:
      return { kind: "pending" as const, item, commitSeq: res.commitSeq };
    case TransitionOutcome.DENIED:
      return { kind: "denied" as const, item, reason: res.reason, commitSeq: res.commitSeq };
    default:
      return { kind: "failed" as const, item, commitSeq: res.commitSeq };
  }
}

// Sends the form: the first time, a draft with its fields, then the move
// to submitted; again (after a refusal), the same draft, its fields carried
// by the move. Waits up to 5 s for HR's check. A refusal keeps the draft
// for the next try; a create that fails throws (there is no draft).
export async function sendRequest(
  api: Pick<Api, "createItem" | "transitionItem">,
  ns: string,
  form: LeaveForm,
  draft?: Item,
): Promise<SendResult> {
  let current = draft;
  let fields: JsonObject | undefined;
  if (!current) {
    const created = await api.createItem({
      parent: ns,
      requestId: crypto.randomUUID(),
      type: `${ns}/types/vacation_request/versions/${REQUEST_VERSION}`,
      fields: leaveFields(form),
    });
    current = created.item!;
  } else {
    fields = { ...leaveFields(form), f_comment: form.reason.trim() === "" ? null : form.reason.trim() };
  }
  try {
    const res = await api.transitionItem({
      name: current.name,
      requestId: crypto.randomUUID(),
      toState: "submitted",
      ...(fields ? { fields } : {}),
      wait: WAIT,
    });
    return outcomeOf(res, current);
  } catch (error) {
    return { kind: "refused", draft: current, error };
  }
}

// --- Deciding ---

export type Decision = "approved" | "rejected";

export type DecideResult =
  | { kind: "applied"; item: Item; commitSeq: bigint; commentSaved: boolean }
  | { kind: "pending"; item: Item; commitSeq: bigint }
  | { kind: "denied"; item: Item; reason: string; commitSeq: bigint }
  | { kind: "failed"; item: Item; commitSeq: bigint };

// The decision form's own rule: a rejection says why.
export function validateDecision(to: Decision, comment: string): { comment?: string } {
  return to === "rejected" && comment.trim() === "" ? { comment: "Say why you're rejecting it." } : {};
}

// The approver's move, with the etag they saw: a request changed since
// (recalled, edited, sent again) comes back STALE and nothing applies.
// Waits up to 5 s for the deduction. The comment is written inside the
// request only once the decision has applied; if that write fails, the
// decision stands without it. A refusal by a rule is thrown.
export async function decide(
  api: Pick<Api, "createItem" | "transitionItem">,
  ns: string,
  request: Item,
  to: Decision,
  comment: string,
): Promise<DecideResult> {
  const res = await api.transitionItem({ name: request.name, requestId: crypto.randomUUID(), etag: request.etag, toState: to, wait: WAIT });
  const out = outcomeOf(res, request);
  if (out.kind !== "sent") return out;
  let commentSaved = true;
  if (comment.trim() !== "") {
    try {
      await api.createItem({
        parent: ns,
        requestId: crypto.randomUUID(),
        type: `${ns}/types/comment/versions/1`,
        container: request.name,
        fields: { f_body: comment.trim() },
      });
    } catch {
      commentSaved = false;
    }
  }
  return { kind: "applied", item: out.item, commitSeq: out.commitSeq, commentSaved };
}

// --- Plain words ---

// Who is reading, whose request it is, and the numbers when known.
export type LeaveWords = { viewer: string; requester: string; asked?: number; left?: number };

// A denial (TRANSITION_OUTCOME_DENIED, or a refused move in the history)
// by its reason, upper or lower case.
export function denialSentence(reason: string, w: LeaveWords): string {
  const mine = w.viewer === w.requester;
  const name = personName(w.requester);
  const numbers = w.asked !== undefined && w.left !== undefined;
  switch (reason.toUpperCase()) {
    case "NO_BALANCE":
      return mine ? "HR hasn't opened a leave balance for you yet. Ask HR to open one, then send it again." : `${name} has no open leave balance.`;
    case "AMBIGUOUS_BALANCE":
      return mine
        ? "You have more than one open leave balance. Ask HR to close the old one, then send it again."
        : `${name} has more than one open leave balance.`;
    case "NO_ELIGIBLE_APPROVER":
      return mine
        ? "Nobody can approve your request: the HR directory names you as your own manager. Ask HR."
        : `Nobody can approve ${name}'s request: the HR directory names ${name} as their own manager.`;
    case "UNKNOWN_EMPLOYEE":
      return mine ? "The HR directory doesn't know you, so nobody can approve your request. Ask HR." : `The HR directory doesn't know ${name}.`;
    case "INSUFFICIENT_BALANCE":
      if (mine) return numbers ? `You don't have enough days left: this asks for ${w.asked} and you have ${w.left}.` : "You don't have enough days left for this.";
      return `${numbers ? `${name} doesn't have enough days left: this asks for ${w.asked} and ${name} has ${w.left}.` : `${name} doesn't have enough days left for this.`} Reject it, or ask HR to add days.`;
    default:
      return `The system refused this (${reason.toLowerCase()}).`;
  }
}

// A move still being checked, by the state asked for.
export function pendingSentence(to: string): string {
  if (to === "submitted") return "Still checking with HR. This page will show the answer.";
  if (to === "approved") return "Still checking the balance. This page will show the answer.";
  return "Still being checked. This page will show the answer.";
}

// The form's words for the request's fields.
export const formWords: Record<string, string> = {
  f_start: "the first day",
  f_end: "the last day",
  f_days: "the number of days",
  f_leave_type: "the leave type",
  f_comment: "the reason",
};

const WHOLE_RULE =
  "The dates and days don't make sense: the last day must be on or after the first, the first can't have passed, and you need at least one day.";

// Today as the form compares dates: the browser's local date.
function localToday(): string {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

// A refusal (a Connect error) in the Requests area's words. For the date
// rule (GUARD_FAILED), the part that failed is named from what was sent:
// an explanation of the server's refusal, never one the app makes.
export function refusalSentence(r: Refusal, ctx: LeaveWords & { sent?: LeaveForm; today?: string }): string {
  switch (r.reason) {
    case "GUARD_FAILED": {
      const s = ctx.sent;
      if (!s) return WHOLE_RULE;
      const today = ctx.today ?? localToday();
      if (s.end < s.start) return "The last day is before the first day.";
      if (s.start < today) return "The first day has already passed.";
      const days = Number(s.days);
      if (!Number.isFinite(days) || days <= 0) return "Ask for at least one day.";
      return WHOLE_RULE;
    }
    case "MISSING_FIELDS": {
      const names = (r.metadata.fields ?? "").split(",").filter((f) => f !== "").map((f) => formWords[f] ?? f);
      return `Fill in ${names.join(" and ")} first.`;
    }
    case "FIELD_FROZEN":
      return r.metadata.state === "submitted"
        ? "This can't be changed while it waits for approval. Recall it first."
        : "This can't be changed any more: it has been decided.";
    case "LINK_UNRESOLVED":
      return denialSentence(r.metadata.reason ?? "", ctx);
    case "STALE":
      return "This request changed since you opened it. It has been reloaded: check it, then decide again.";
  }
  return explain(r);
}
