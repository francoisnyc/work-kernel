// Waiting for you: the requests whose approver I am, oldest first, with how
// long each has waited. One history read per row says when it entered
// submitted and whether an approval was refused since.
import type { ReactElement } from "react";
import { useNow } from "../../app/poll";
import { idOf } from "../../app/router";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { inboxOrder, inboxRow, inboxSearch, waitedLabel } from "../../model/inbox";
import { denialSentence, requesterOf } from "../../model/leave";
import { personName } from "../../model/people";
import { Empty, Loading, PageHeader } from "../../ui/Page";
import { FlashStatus, RequestsFrame } from "./RequestsRoutes";
import { datesLabel, daysOf, leaveTypeLabel, readHistory, reasonOf } from "./shared";

export function InboxPage(): ReactElement {
  const s = useSession();
  const now = useNow(30_000);
  const load = useLoad(async () => {
    const res = await s.api.searchItems({ ...inboxSearch(s.ns), ...s.readYourWrites });
    const rows = await Promise.all(res.items.map(async (i) => inboxRow(i, await readHistory(s, i.name))));
    return { rows: inboxOrder(rows), more: res.nextPageToken !== "" };
  }, []);

  return (
    <RequestsFrame current="inbox">
      <PageHeader title="Waiting for you" eyebrow="Requests" load={load}>
        <p className="muted">Requests you are the approver of, oldest first. Waiting times are on the wall clock.</p>
      </PageHeader>
      <FlashStatus />
      <Loading load={load}>
        {(d) =>
          d.rows.length === 0 ? (
            <Empty title="Nothing is waiting for you">When HR names you as someone's approver, their request lands here.</Empty>
          ) : (
            <div className="table-scroll">
              <table className="table">
                <caption className="sr-only">Requests waiting for you</caption>
                <thead>
                  <tr>
                    <th scope="col">Requester</th>
                    <th scope="col">Dates</th>
                    <th scope="col">Days</th>
                    <th scope="col">Leave type</th>
                    <th scope="col">Reason</th>
                    <th scope="col">Waiting (wall clock)</th>
                    <th scope="col">Note</th>
                    <th scope="col">
                      <span className="sr-only">Decide</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {d.rows.map((r) => {
                    const who = requesterOf(r.item);
                    return (
                      <tr key={r.item.name}>
                        <td>{personName(who)}</td>
                        <td>{datesLabel(r.item)}</td>
                        <td className="nums">{daysOf(r.item)}</td>
                        <td>{leaveTypeLabel(r.item)}</td>
                        <td>{reasonOf(r.item)}</td>
                        <td className="nums" title={r.waitingSince}>
                          {waitedLabel(r.waitingSince, now)}
                        </td>
                        <td>
                          {r.checking ? (
                            <span className="badge tone-waiting">Being checked</span>
                          ) : r.refused ? (
                            <span className="muted">{denialSentence(r.refused, { viewer: s.me, requester: who })}</span>
                          ) : null}
                        </td>
                        <td>
                          <a className="button button-small" href={`#/requests/task/${idOf(r.item.name)}`}>
                            Review<span className="sr-only"> {personName(who)}'s request</span>
                          </a>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )
        }
      </Loading>
      {load.data?.more && <p className="muted">More than 500 requests wait for you: only the first 500 are shown.</p>}
    </RequestsFrame>
  );
}
