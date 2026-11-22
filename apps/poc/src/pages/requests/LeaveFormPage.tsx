// Request time off: the dates, the days, the leave type and a reason. The
// form checks blanks only; the server judges the dates (the Type's guard)
// and the HR check finds the approver and the balance. A refused send keeps
// its draft, and the next send moves that same draft.
import type { Item } from "@gen/kernel/v1/kernel_pb";
import { useState, type ReactElement } from "react";
import { go, idOf } from "../../app/router";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { optionLabel } from "../../model/labels";
import {
  balanceOf,
  balanceSearch,
  balanceWarning,
  blanks,
  denialSentence,
  refusalSentence,
  sendRequest,
  suggestDays,
  unpaidNote,
  type LeaveForm,
} from "../../model/leave";
import { refusalOf } from "../../model/refusal";
import { Icon } from "../../ui/Icon";
import { Loading, PageHeader } from "../../ui/Page";
import { RefusalNotice } from "../../ui/RefusalNotice";
import { RequestsFrame } from "./RequestsRoutes";
import { BalanceSummary } from "./shared";

const LEAVE_TYPES = ["annual", "unpaid", "other"];

export function LeaveFormPage({ draftId }: { draftId?: string }): ReactElement {
  const s = useSession();
  const load = useLoad(async () => {
    const [balances, draft] = await Promise.all([
      s.api.searchItems({ ...balanceSearch(s.ns), ...s.readYourWrites }).then((r) => r.items),
      draftId ? s.api.getItem({ name: `${s.ns}/items/${draftId}` }).then((r) => r.item) : Promise.resolve(undefined),
    ]);
    return { balance: balances[0] ? balanceOf(balances[0]) : undefined, draft };
  }, [draftId], { poll: false });

  return (
    <RequestsFrame current="none">
      <PageHeader title="Request time off" eyebrow="Requests" />
      <Loading load={load}>{(d) => <Form key={d.draft?.name ?? "new"} draft={d.draft} balance={d.balance} />}</Loading>
    </RequestsFrame>
  );
}

function initial(draft?: Item): LeaveForm {
  const f = draft?.fields ?? {};
  const text = (v: unknown) => (typeof v === "string" ? v : "");
  return { start: text(f.f_start), end: text(f.f_end), days: text(f.f_days), leaveType: text(f.f_leave_type) || "annual", reason: text(f.f_comment) };
}

function Form({ draft: given, balance }: { draft?: Item; balance?: ReturnType<typeof balanceOf> }): ReactElement {
  const s = useSession();
  const [form, setForm] = useState<LeaveForm>(() => initial(given));
  // A draft's days were typed by someone: they stay when a date changes.
  const [typed, setTyped] = useState(given !== undefined && initial(given).days !== "");
  const [hint, setHint] = useState<string>();
  const [problems, setProblems] = useState<ReturnType<typeof blanks>>({});
  const [draft, setDraft] = useState<Item | undefined>(given);
  const [refusal, setRefusal] = useState<{ error?: unknown; text: string }>();
  const [busy, setBusy] = useState(false);

  const setDate = (k: "start" | "end") => (e: { target: { value: string } }) => {
    const next = { ...form, [k]: e.target.value };
    const sug = suggestDays(next.start, next.end, next.days, typed);
    setForm({ ...next, days: sug.days });
    setHint(sug.hint);
  };
  const days = Number(form.days);
  const warning = form.days.trim() !== "" && Number.isFinite(days) ? balanceWarning(balance, days) : undefined;
  const note = unpaidNote(form.leaveType);
  const words = { viewer: s.me, requester: s.me };

  const error = (k: keyof LeaveForm) => problems[k] && (
    <p id={`leave-${k}-error`} className="field-error">
      {problems[k]}
    </p>
  );
  const described = (k: keyof LeaveForm, hintId?: string) => (problems[k] ? `leave-${k}-error` : hintId);

  return (
    <div className="form-layout">
      <form
        className="card"
        noValidate
        onSubmit={async (e) => {
          e.preventDefault();
          const p = blanks(form);
          setProblems(p);
          if (Object.keys(p).length > 0) return;
          setBusy(true);
          setRefusal(undefined);
          try {
            const sent = { ...form };
            const out = await sendRequest(s.api, s.ns, sent, draft);
            switch (out.kind) {
              case "sent":
              case "pending":
                s.wrote(out);
                go(`#/requests/case/${idOf(out.item.name)}`);
                return;
              case "denied":
                s.wrote(out);
                setDraft(out.item);
                setRefusal({ text: denialSentence(out.reason, words) });
                return;
              case "failed":
                s.wrote(out);
                setDraft(out.item);
                setRefusal({ text: "The HR check could not answer. Try again in a moment." });
                return;
              case "refused":
                setDraft(out.draft);
                setRefusal({ error: out.error, text: refusalSentence(refusalOf(out.error), { ...words, sent }) });
                return;
            }
          } catch (err) {
            setRefusal({ error: err, text: refusalSentence(refusalOf(err), words) });
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="field-row">
          <div className="field">
            <label htmlFor="leave-start">First day</label>
            <input
              id="leave-start"
              type="date"
              value={form.start}
              onChange={setDate("start")}
              aria-invalid={problems.start ? true : undefined}
              aria-describedby={described("start")}
            />
            {error("start")}
          </div>
          <div className="field">
            <label htmlFor="leave-end">Last day</label>
            <input id="leave-end" type="date" value={form.end} onChange={setDate("end")} aria-invalid={problems.end ? true : undefined} aria-describedby={described("end")} />
            {error("end")}
          </div>
          <div className="field">
            <label htmlFor="leave-days">Days</label>
            <input
              id="leave-days"
              inputMode="numeric"
              value={form.days}
              onChange={(e) => {
                setTyped(true);
                setForm({ ...form, days: e.target.value });
              }}
              aria-invalid={problems.days ? true : undefined}
              aria-describedby={described("days", "leave-days-hint")}
            />
            {error("days") ?? (
              <p id="leave-days-hint" className="field-hint">
                {hint ?? "Working days, suggested from the dates."}
              </p>
            )}
          </div>
        </div>
        <div className="field">
          <label htmlFor="leave-type">Leave type</label>
          <select id="leave-type" value={form.leaveType} onChange={(e) => setForm({ ...form, leaveType: e.target.value })} aria-describedby={note ? "leave-type-note" : undefined}>
            {LEAVE_TYPES.map((t) => (
              <option key={t} value={t}>
                {optionLabel("f_leave_type", t)}
              </option>
            ))}
          </select>
          {note && (
            <p id="leave-type-note" className="field-hint">
              {note}
            </p>
          )}
        </div>
        <div className="field">
          <label htmlFor="leave-reason">Reason (optional)</label>
          <textarea id="leave-reason" rows={3} value={form.reason} onChange={(e) => setForm({ ...form, reason: e.target.value })} />
        </div>
        {warning && (
          <p className="notice notice-warning">
            <Icon name="alert" /> {warning}
          </p>
        )}
        {refusal &&
          (refusal.error !== undefined ? (
            <RefusalNotice error={refusal.error} text={refusal.text} />
          ) : (
            <div className="notice notice-danger" role="alert">
              <p className="notice-text">
                <Icon name="alert" />
                <span>{refusal.text}</span>
              </p>
            </div>
          ))}
        {draft && refusal && <p className="muted">Your request is kept as a draft: fix it here and send it again.</p>}
        <div className="button-row">
          <button type="submit" className="button button-primary" disabled={busy}>
            Send for approval
          </button>
          <a href={draft ? `#/requests/case/${idOf(draft.name)}` : "#/requests"}>{draft ? "Open the draft" : "Cancel"}</a>
        </div>
      </form>
      <aside className="form-aside">
        <BalanceSummary title="Your leave balance" balance={balance} missing="HR hasn't opened a leave balance for you yet." />
        <p className="muted">Your manager decides, as the HR directory names them. A request waiting too long expires.</p>
      </aside>
    </div>
  );
}
