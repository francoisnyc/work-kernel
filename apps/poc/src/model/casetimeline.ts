// A case's timeline: who did what when. It reads the request's history,
// the decision comments inside it, and what its reactions wrote on the
// balance (that history is the balance's, read only by its readers).
//
// A move the kernel finished (after the HR check, after the deduction) is
// said as the person's who asked for it (the event's on_behalf_of); what
// the kernel did by itself is a system step: who the HR check named, the
// balance it linked, reminders, emails, days taken and given back, expiry.
import { ItemEventKind, type Item, type ItemEvent, type Type } from "@gen/kernel/v1/kernel_pb";
import { iso } from "./inbox";
import { fieldLabel, humanize, stateLabel } from "./labels";
import { formWords } from "./leave";
import { personName } from "./people";
import { durationLabel } from "./process";

export type CaseEntry = { at: string; who: string; text: string; system: boolean; refused: boolean };

export type CaseContext = {
  type: Type;
  // The request's name, namespaces/{ns}/items/{id}.
  request: string;
  requester: string;
  viewer: string;
  // The balance's history, when the viewer can read it.
  balanceEvents?: ItemEvent[];
  // The decision comments inside the request.
  comments?: Item[];
};

const isPerson = (p: string) => /^(user|customer|agent):/.test(p);
const lastPart = (s: string) => s.slice(s.lastIndexOf(":") + 1);
const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

// The vacation request's verbs; any other Type's moves are "moved it to".
const asks: Record<string, string> = {
  submitted: "send it for approval",
  approved: "approve it",
  rejected: "reject it",
  draft: "recall it",
  withdrawn: "withdraw it",
};
const dones: Record<string, string> = {
  submitted: "sent it for approval",
  approved: "approved it",
  rejected: "rejected it",
  draft: "recalled it",
  withdrawn: "withdrew it",
};
const reasons: Record<string, string> = {
  insufficient_balance: "not enough days left",
  no_balance: "no open leave balance",
  ambiguous_balance: "more than one open leave balance",
  no_eligible_approver: "no one can approve it",
  unknown_employee: "not in the HR directory",
};
const checks: Record<string, string> = { hr_check: "The HR check" };
const emails: Record<string, string> = {
  notify_approved: "approved",
  notify_rejected: "rejected",
  notify_expired: "expired",
  refund_email: "days given back",
};
const timerMoves: Record<string, (after: string) => string> = { expired: (d) => `Expired: nobody decided within ${d}` };

export function caseTimeline(events: ItemEvent[], ctx: CaseContext): CaseEntry[] {
  const vacation = ctx.type.typeId === "vacation_request";
  const state = (s: string) => stateLabel(ctx.type.typeId, s);
  const ask = (to: string) => (vacation && asks[to]) || `move it to ${state(to)}`;
  const done = (to: string) => (vacation && dones[to]) || `moved it to ${state(to)}`;
  const who = (p: string) => (p === ctx.viewer ? "You" : personName(p));
  const whom = (p: string) => (p === ctx.viewer ? "you" : personName(p));
  const whose = (p: string) => (p === ctx.viewer ? "your" : `${personName(p)}'s`);
  const reason = (r: string) => reasons[r.toLowerCase()] ?? humanize(r).toLowerCase();

  const out: CaseEntry[] = [];
  const add = (at: string, by: string, text: string, system: boolean, refused = false) => out.push({ at, who: by, text, system, refused });
  const sys = (at: string, text: string) => add(at, "System", text, true);
  const resolved = new Set(
    events
      .filter((e) => e.hold !== "" && [ItemEventKind.TRANSITIONED, ItemEventKind.TRANSITION_DENIED, ItemEventKind.TRANSITION_FAILED].includes(e.kind))
      .map((e) => e.hold),
  );
  const maxRuns = (timer: string) => ctx.type.states.flatMap((s) => s.acting).find((a) => a.id === timer)?.max;

  for (const ev of events) {
    const at = iso(ev.createTime);
    const person = isPerson(ev.actor) ? ev.actor : ev.onBehalfOf;
    switch (ev.kind) {
      case ItemEventKind.CREATED:
        add(at, who(person || ev.actor), "created the request", false);
        break;
      case ItemEventKind.TRANSITION_REQUESTED:
        if (!resolved.has(ev.hold)) add(at, who(person), `asked to ${ask(ev.requestedState)}; the system is checking`, false);
        break;
      case ItemEventKind.TRANSITIONED: {
        if (ev.actor.startsWith("system:")) {
          const timer = ctx.type.states.find((s) => s.id === ev.fromState)?.timer;
          const after = timer && timer.toState === ev.toState ? durationLabel(timer.after) : "";
          const words = vacation ? timerMoves[ev.toState] : undefined;
          sys(at, words && after ? words(after) : `Moved to ${state(ev.toState)}${after ? ` after ${after}` : ""}`);
        } else if (person) add(at, who(person), done(ev.toState), false);
        else sys(at, `Moved to ${state(ev.toState)}`);
        // What a check wrote on the way in, and the link the kernel set.
        if (ev.actor.startsWith("hook:")) {
          const check = checks[lastPart(ev.actor)] ?? "The system";
          for (const [f, v] of Object.entries(ev.fields ?? {}))
            sys(at, f === "f_approver" && typeof v === "string" ? `${check} named ${whom(v)} as approver` : `${check} set ${fieldLabel(f).toLowerCase()}`);
        }
        if (ev.linkType !== "" && ev.linkTarget)
          sys(at, ev.linkType === "balance" ? `Linked to ${whose(ctx.requester)} leave balance` : `Linked: ${humanize(ev.linkType).toLowerCase()}`);
        break;
      }
      case ItemEventKind.TRANSITION_DENIED:
        add(at, who(person), `tried to ${ask(ev.requestedState)}: ${reason(ev.reason)}`, false, true);
        break;
      case ItemEventKind.TRANSITION_FAILED:
        add(at, who(person), `tried to ${ask(ev.requestedState)}: the check failed`, false, true);
        break;
      case ItemEventKind.UPDATED: {
        if (!isPerson(ev.actor)) break;
        const fields = Object.keys(ev.fields ?? {}).map((f) => formWords[f] ?? fieldLabel(f).toLowerCase());
        if (fields.length > 0) add(at, who(ev.actor), `changed ${fields.join(" and ")}`, false);
        break;
      }
      case ItemEventKind.HOOK_RAN:
      case ItemEventKind.HOOK_FAILED: {
        const ok = ev.kind === ItemEventKind.HOOK_RAN;
        if (ev.hook === "remind_approver") {
          const max = maxRuns(ev.timer);
          sys(at, ok ? `Reminder sent to the approver (${ev.run}${max ? ` of ${max}` : ""})` : "Couldn't send the reminder to the approver");
        } else if (emails[ev.hook]) {
          sys(at, ok ? `Email sent to ${whom(ctx.requester)}: ${emails[ev.hook]}` : `Couldn't send the email to ${whom(ctx.requester)}: ${emails[ev.hook]}`);
        } else sys(at, `${ok ? "Ran" : "Couldn't run"} ${humanize(ev.hook).toLowerCase()}`);
        break;
      }
      case ItemEventKind.REACTION_APPLIED:
        // The amount is on the balance; without its history, say what happened.
        if (!ctx.balanceEvents) sys(at, lastPart(ev.reaction) === "refund" ? "Days given back to the balance" : `Ran ${humanize(lastPart(ev.reaction)).toLowerCase()}`);
        break;
      case ItemEventKind.REACTION_FAILED:
        sys(at, `Couldn't run ${humanize(lastPart(ev.reaction)).toLowerCase()}`);
        break;
      default:
        break;
    }
  }

  // What this request's reactions wrote on the balance: a write caused by
  // an event of this history (its causation_id is <request id>:<seq>) that
  // moves, or asks to move, into the reaction's state (the approval's ask
  // for the deduction, the withdrawal for the refund).
  const id = ctx.request.slice(ctx.request.lastIndexOf("/") + 1);
  const bySeq = new Map(events.map((e) => [`${id}:${e.itemSeq}`, e]));
  for (const ev of ctx.balanceEvents ?? []) {
    if (!ev.actor.startsWith("reaction:")) continue;
    const cause = bySeq.get(ev.causationId);
    const reactionState = ctx.type.reactions.find((r) => r.id === lastPart(ev.actor))?.state;
    if (!cause || !reactionState || (cause.toState !== reactionState && cause.requestedState !== reactionState)) continue;
    const n = Number((ev.added ?? {}).f_used ?? 0);
    const used = String((ev.fields ?? {}).f_used ?? "");
    const tail = `${whose(ctx.requester)} balance${used !== "" ? ` (${used} used)` : ""}`;
    if (n > 0) sys(iso(ev.createTime), `${plural(n, "day")} taken from ${tail}`);
    else if (n < 0) sys(iso(ev.createTime), `${plural(-n, "day")} given back to ${tail}`);
  }

  // A decision comment follows its author's move (decide writes it once
  // the move applied): shown once the history has that move.
  const movers = events.filter((e) => e.kind === ItemEventKind.TRANSITIONED).map((e) => ({ by: isPerson(e.actor) ? e.actor : e.onBehalfOf, at: iso(e.createTime) }));
  for (const c of (ctx.comments ?? []).filter((c) => movers.some((m) => m.by === (c.creatorOnBehalfOf || c.creator) && m.at <= iso(c.createTime))))
    add(iso(c.createTime), who(c.creatorOnBehalfOf || c.creator), `commented: "${String(c.fields?.f_body ?? "")}"`, false);

  // Oldest first; entries of one instant keep their order.
  return out.map((e, i) => ({ e, i })).sort((a, b) => (a.e.at < b.e.at ? -1 : a.e.at > b.e.at ? 1 : a.i - b.i)).map((x) => x.e);
}
