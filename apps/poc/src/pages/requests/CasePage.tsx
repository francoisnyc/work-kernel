// A case: the request, where it stands on its Type's process map, the moves
// the viewer may take, and the timeline (the deduction from the balance
// included, when the viewer can read the balance).
import type { ItemEvent } from "@gen/kernel/v1/kernel_pb";
import type { ReactElement } from "react";
import { idOf } from "../../app/router";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { caseTimeline } from "../../model/casetimeline";
import { inboxRow } from "../../model/inbox";
import { stateLabel } from "../../model/labels";
import { denialSentence, pendingSentence, requestStatus, requesterOf } from "../../model/leave";
import { movesFor } from "../../model/moves";
import { personName } from "../../model/people";
import { layoutProcess, progressOf } from "../../model/process";
import { Fact } from "../../ui/Facts";
import { when } from "../../ui/format";
import { Icon } from "../../ui/Icon";
import { MoveButtons } from "../../ui/MoveButtons";
import { Loading, PageHeader, StatePill } from "../../ui/Page";
import { ProcessMap } from "../../ui/ProcessMap";
import { RequestsFrame } from "./RequestsRoutes";
import { CaseTimelineView, datesLabel, daysOf, leaveTypeLabel, readHistory, reasonOf } from "./shared";

export function CasePage({ id }: { id: string }): ReactElement {
  const s = useSession();
  const name = `${s.ns}/items/${id}`;
  const load = useLoad(async () => {
    const item = (await s.api.getItem({ name })).item!;
    const link = item.links.find((l) => l.linkType === "balance");
    const balanceName = link?.item && !link.item.restricted ? link.item.name : undefined;
    const [type, events, comments, balanceEvents] = await Promise.all([
      s.type(item.type),
      readHistory(s, name),
      s.api.listItems({ parent: name, filter: { types: [`${s.ns}/types/comment`] }, pageSize: 100 }).then((r) => r.items),
      // The balance's history: only when the viewer can read the balance.
      balanceName ? readHistory(s, balanceName).catch((): ItemEvent[] | undefined => undefined) : Promise.resolve(undefined),
    ]);
    return { item, type, events, comments, balanceEvents };
  }, [id]);

  return (
    <RequestsFrame current="none">
      <Loading load={load}>
        {(d) => {
          const requester = requesterOf(d.item);
          const vacation = d.type.typeId === "vacation_request";
          const refused = d.item.state === "submitted" ? inboxRow(d.item, d.events).refused : undefined;
          const offered = movesFor(d.type, d.item, { principal: s.me, roles: s.user.roles });
          const decides = offered.some((m) => m.to === "approved" || m.to === "rejected");
          const edits = d.item.state === "draft" && offered.some((m) => m.to === "submitted");
          const buttons = offered.filter((m) => !["approved", "rejected", "submitted"].includes(m.to));
          const map = layoutProcess(d.type, progressOf(d.type, d.item.state, d.events));
          const entries = caseTimeline(d.events, { type: d.type, request: name, requester, viewer: s.me, balanceEvents: d.balanceEvents, comments: d.comments });
          return (
            <>
              <PageHeader title={vacation ? "Leave request" : stateLabel(d.type.typeId, d.item.state)} eyebrow={`Requests · ${requester === s.me ? "your" : `${personName(requester)}'s`} request`} load={load} />
              <div className="stack">
                {d.item.hold && (
                  <p role="status" className="notice">
                    <Icon name="clock" /> {pendingSentence(d.item.hold.requestedState)}
                  </p>
                )}
                {refused && (
                  <p className="notice notice-warning">
                    <Icon name="alert" /> {denialSentence(refused, { viewer: s.me, requester, asked: Number(d.item.fields?.f_days) })}
                  </p>
                )}
                <div className="detail">
                  <section className="card" aria-labelledby="case-facts">
                    <h2 id="case-facts" className="sr-only">
                      The request
                    </h2>
                    <dl className="facts">
                      <Fact term="Status">
                        <StatePill category={d.item.category} label={requestStatus(d.item)} />
                      </Fact>
                      <Fact term="Requester">{personName(requester)}</Fact>
                      <Fact term="Dates">{datesLabel(d.item)}</Fact>
                      <Fact term="Days">{daysOf(d.item)}</Fact>
                      <Fact term="Leave type">{leaveTypeLabel(d.item)}</Fact>
                      <Fact term="Reason">{reasonOf(d.item) || <span className="muted">None given</span>}</Fact>
                      <Fact term="Approver">
                        {typeof d.item.fields?.f_approver === "string" ? personName(d.item.fields.f_approver) : <span className="muted">Named by HR when it is sent</span>}
                      </Fact>
                      <Fact term="Asked">{when(d.item.createTime)}</Fact>
                    </dl>
                  </section>
                  <section className="card" aria-labelledby="case-moves">
                    <h2 id="case-moves">What you can do</h2>
                    {decides && (
                      <p>
                        <a className="button button-primary" href={`#/requests/task/${idOf(name)}`}>
                          Decide
                        </a>
                      </p>
                    )}
                    {edits && (
                      <p>
                        <a className="button button-primary" href={`#/requests/edit/${idOf(name)}`}>
                          Edit and send
                        </a>
                      </p>
                    )}
                    {buttons.length > 0 ? (
                      <MoveButtons
                        moves={buttons}
                        options={{}}
                        ctx={{ guard: d.item.state === "approved" ? "An approved request can be withdrawn only before the leave starts." : undefined }}
                        onMove={async (to) => {
                          s.wrote(await s.api.transitionItem({ name, requestId: crypto.randomUUID(), etag: d.item.etag, toState: to, wait: { seconds: 5n } }));
                          load.reload();
                        }}
                      />
                    ) : (
                      !decides && !edits && <p className="muted">Nothing for you to do here.</p>
                    )}
                  </section>
                </div>
                <section className="section" aria-labelledby="case-process">
                  <h2 id="case-process">Process</h2>
                  <p className="muted">
                    Drawn from the Type ({d.type.typeId}, version {d.type.version}). Durations are as the Type declares them; the dev stack runs this Type's timers fast.
                  </p>
                  <ProcessMap map={map} name={vacation ? "Leave request" : d.type.typeId} />
                </section>
                <CaseTimelineView entries={entries} />
              </div>
            </>
          );
        }}
      </Loading>
    </RequestsFrame>
  );
}
