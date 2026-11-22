// The decision: a request, the requester's balance and their other
// requests in the same period; approve or reject with a comment. The move
// carries the etag read here, so a request changed since (recalled, edited,
// sent again) comes back STALE and is read again, never decided unseen.
import type { Item } from "@gen/kernel/v1/kernel_pb";
import { useState, type ReactElement } from "react";
import { go, idOf } from "../../app/router";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import {
  balanceOf,
  decide,
  denialSentence,
  othersInPeriod,
  overlapSearch,
  pendingSentence,
  refusalSentence,
  requestStatus,
  requesterOf,
  validateDecision,
  type Decision,
} from "../../model/leave";
import { movesFor } from "../../model/moves";
import { personName } from "../../model/people";
import { refusalOf } from "../../model/refusal";
import { Fact } from "../../ui/Facts";
import { when } from "../../ui/format";
import { Icon } from "../../ui/Icon";
import { Loading, PageHeader, StatePill } from "../../ui/Page";
import { RefusalNotice } from "../../ui/RefusalNotice";
import { RequestsFrame, useFlash } from "./RequestsRoutes";
import { BalanceSummary, datesLabel, daysOf, leaveTypeLabel, reasonOf } from "./shared";

type Said = { tone: "alert" | "status"; text: string; error?: unknown };

export function DecisionPage({ id }: { id: string }): ReactElement {
  const s = useSession();
  const name = `${s.ns}/items/${id}`;
  const [comment, setComment] = useState("");
  const load = useLoad(
    async () => {
      const item = (await s.api.getItem({ name })).item!;
      const [type, balanceItem, found] = await Promise.all([
        s.type(item.type),
        (async () => {
          const link = item.links.find((l) => l.linkType === "balance");
          return link?.item && !link.item.restricted ? s.item(link.item.name, true) : undefined;
        })(),
        typeof item.fields?.f_start === "string" && typeof item.fields?.f_end === "string"
          ? s.api.searchItems({ ...overlapSearch(s.ns, item.fields.f_start, item.fields.f_end), ...s.readYourWrites }).then((r) => r.items)
          : Promise.resolve([] as Item[]),
      ]);
      return { item, type, balance: balanceItem ? balanceOf(balanceItem) : undefined, others: othersInPeriod(found, item) };
    },
    [id],
    { paused: comment !== "" },
  );
  const requester = load.data ? requesterOf(load.data.item) : "";
  const who = personName(requester);

  return (
    <RequestsFrame current="inbox">
      <PageHeader title={load.data ? `${who}'s request` : "Request"} eyebrow="Requests · decision" load={load} />
      <Loading load={load}>
        {(d) => (
          <div className="detail">
            <div className="stack">
              <section className="card" aria-labelledby="decision-request">
                <h2 id="decision-request">Leave asked for</h2>
                <dl className="facts">
                  <Fact term="Status">
                    <StatePill category={d.item.category} label={requestStatus(d.item)} />
                  </Fact>
                  <Fact term="Dates">{datesLabel(d.item)}</Fact>
                  <Fact term="Days">{daysOf(d.item)}</Fact>
                  <Fact term="Leave type">{leaveTypeLabel(d.item)}</Fact>
                  <Fact term="Reason">{reasonOf(d.item) || <span className="muted">None given</span>}</Fact>
                  <Fact term="Asked">{when(d.item.createTime)}</Fact>
                </dl>
                <p>
                  <a href={`#/requests/case/${id}`}>Open the case: its process and timeline</a>
                </p>
              </section>
              <DecisionForm item={d.item} type={d.type} balanceLeft={d.balance?.left} comment={comment} setComment={setComment} reload={load.reload} />
            </div>
            <div className="stack">
              <BalanceSummary
                title={`${who}'s leave balance`}
                balance={d.balance}
                missing={`You can't see ${who}'s balance: HR has not made you one of its readers. Approving still checks it.`}
              />
              <section className="card" aria-labelledby="decision-others">
                <h2 id="decision-others">{`${who}'s other requests in this period`}</h2>
                {d.others.length === 0 ? (
                  <p className="muted">None</p>
                ) : (
                  <ul className="plain rows">
                    {d.others.map((o) => (
                      <li key={o.name}>
                        <a href={`#/requests/case/${idOf(o.name)}`}>{datesLabel(o)}</a> · {daysOf(o)} days · {requestStatus(o)}
                      </li>
                    ))}
                  </ul>
                )}
              </section>
            </div>
          </div>
        )}
      </Loading>
    </RequestsFrame>
  );
}

function DecisionForm({
  item,
  type,
  balanceLeft,
  comment,
  setComment,
  reload,
}: {
  item: Item;
  type: Awaited<ReturnType<ReturnType<typeof useSession>["type"]>>;
  balanceLeft?: number;
  comment: string;
  setComment: (c: string) => void;
  reload: () => void;
}): ReactElement {
  const s = useSession();
  const flash = useFlash();
  const [busy, setBusy] = useState(false);
  const [said, setSaid] = useState<Said>();
  const [problem, setProblem] = useState<string>();
  const requester = requesterOf(item);
  const words = { viewer: s.me, requester };
  const offered = movesFor(type, item, { principal: s.me, roles: s.user.roles }).map((m) => m.to);
  const canApprove = offered.includes("approved");
  const canReject = offered.includes("rejected");

  if (item.state !== "submitted")
    return (
      <p className="notice notice-warning">
        This request is no longer waiting for a decision: it is {requestStatus(item).toLowerCase()}.
      </p>
    );
  if (!canApprove && !canReject)
    return (
      <p className="notice notice-warning">
        {typeof item.fields?.f_approver === "string"
          ? `${personName(item.fields.f_approver)} decides this request, not you.`
          : "No approver has been named for this request yet."}
      </p>
    );

  async function act(to: Decision) {
    const p = validateDecision(to, comment).comment;
    setProblem(p);
    if (p) return;
    setBusy(true);
    setSaid(undefined);
    try {
      const out = await decide(s.api, s.ns, item, to, comment);
      switch (out.kind) {
        case "applied":
          s.wrote(out);
          flash.set(
            `You ${to === "approved" ? "approved" : "rejected"} ${personName(requester)}'s request.${out.commentSaved ? "" : " Your comment could not be saved."}`,
            "inbox",
          );
          go("#/requests/inbox");
          return;
        case "pending":
          s.wrote(out);
          setSaid({ tone: "status", text: pendingSentence(to) });
          setTimeout(reload, 2000);
          return;
        case "denied":
          s.wrote(out);
          setSaid({ tone: "alert", text: denialSentence(out.reason, { ...words, asked: Number(item.fields?.f_days), left: balanceLeft }) });
          reload();
          return;
        case "failed":
          s.wrote(out);
          setSaid({ tone: "alert", text: "The balance check could not answer. Try again in a moment." });
          return;
      }
    } catch (err) {
      const r = refusalOf(err);
      setSaid({ tone: "alert", text: refusalSentence(r, words), error: err });
      if (r.reason === "STALE") reload();
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="card" aria-labelledby="decision-title">
      <h2 id="decision-title">Your decision</h2>
      <div className="field">
        <label htmlFor="decision-comment">Comment</label>
        <textarea
          id="decision-comment"
          rows={3}
          value={comment}
          onChange={(e) => setComment(e.target.value)}
          aria-invalid={problem ? true : undefined}
          aria-describedby={problem ? "decision-comment-error" : "decision-comment-hint"}
        />
        {problem ? (
          <p id="decision-comment-error" className="field-error">
            {problem}
          </p>
        ) : (
          <p id="decision-comment-hint" className="field-hint">
            Needed to reject. {personName(requester)} sees it on the request, after your decision.
          </p>
        )}
      </div>
      {said &&
        (said.tone === "status" ? (
          <p role="status" className="notice">
            {said.text}
          </p>
        ) : said.error !== undefined ? (
          <RefusalNotice error={said.error} text={said.text} />
        ) : (
          <div className="notice notice-danger" role="alert">
            <p className="notice-text">
              <Icon name="alert" />
              <span>{said.text}</span>
            </p>
          </div>
        ))}
      <div className="button-row">
        {canApprove && (
          <button type="button" className="button button-primary" disabled={busy} onClick={() => void act("approved")}>
            <Icon name="check" />
            Approve
          </button>
        )}
        {canReject && (
          <button type="button" className="button" disabled={busy} onClick={() => void act("rejected")}>
            <Icon name="x" />
            Reject
          </button>
        )}
      </div>
    </section>
  );
}
