// My leave: my balance, a way to ask for time off, and my requests with
// where each stands. There is no filter on the creator: my requests are
// picked out of every request I can read (up to three pages).
import type { Item } from "@gen/kernel/v1/kernel_pb";
import type { ReactElement } from "react";
import { idOf } from "../../app/router";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { inboxRow } from "../../model/inbox";
import { balanceOf, balanceSearch, byRequester, denialSentence, pendingDays, requestStatus, requestsSearch } from "../../model/leave";
import { Icon } from "../../ui/Icon";
import { Empty, Loading, PageHeader, StatePill } from "../../ui/Page";
import { RequestsFrame } from "./RequestsRoutes";
import { BalanceSummary, datesLabel, daysOf, leaveTypeLabel, readHistory, reasonOf } from "./shared";

const PAGES = 3;

export function MyLeave(): ReactElement {
  const s = useSession();
  const load = useLoad(async () => {
    const balances = (await s.api.searchItems({ ...balanceSearch(s.ns), ...s.readYourWrites })).items;
    let all: Item[] = [];
    let token = "";
    for (let p = 0; p < PAGES; p++) {
      const res = await s.api.searchItems({ ...requestsSearch(s.ns, token), ...s.readYourWrites });
      all = [...all, ...res.items];
      token = res.nextPageToken;
      if (token === "") break;
    }
    const mine = byRequester(all, s.me);
    // A request waiting for approval says why an approval was refused.
    const refused = new Map<string, string>();
    await Promise.all(
      mine
        .filter((r) => r.state === "submitted")
        .map(async (r) => {
          const why = inboxRow(r, await readHistory(s, r.name)).refused;
          if (why) refused.set(r.name, why);
        }),
    );
    return { balances, mine, refused, more: token !== "" };
  }, []);

  return (
    <RequestsFrame current="mine">
      <PageHeader
        title="My leave"
        eyebrow="Requests"
        load={load}
        actions={
          <a className="button button-primary" href="#/requests/new">
            <Icon name="plus" />
            Request time off
          </a>
        }
      />
      <div className="stack">
        <Loading load={load}>
          {(d) => (
            <>
              <BalanceSummary
                title="Your leave balance"
                balance={d.balances[0] ? balanceOf(d.balances[0]) : undefined}
                pending={pendingDays(d.mine)}
                missing="HR hasn't opened a leave balance for you yet."
              />
              {d.balances.length > 1 && (
                <p className="notice notice-warning">You have {d.balances.length} open leave balances. Ask HR to close the old one: a request is refused until then.</p>
              )}
            </>
          )}
        </Loading>
        {/* The table is drawn at once, its rows when the first read answers. */}
        <section className="section" aria-labelledby="my-requests-title">
          <h2 id="my-requests-title">My requests</h2>
          {load.data && load.data.mine.length === 0 ? (
            <Empty title="No requests yet">Ask for time off and it shows here, with where it stands.</Empty>
          ) : (
            <div className="table-scroll">
              <table className="table" aria-busy={load.data ? undefined : true}>
                <caption className="sr-only">My requests</caption>
                <thead>
                  <tr>
                    <th scope="col">Dates</th>
                    <th scope="col">Days</th>
                    <th scope="col">Leave type</th>
                    <th scope="col">Reason</th>
                    <th scope="col">Status</th>
                    <th scope="col">Note</th>
                  </tr>
                </thead>
                <tbody>
                  {!load.data ? (
                    <tr>
                      <td colSpan={6} className="muted">
                        {load.error ? "Your requests could not be read." : "Reading your requests…"}
                      </td>
                    </tr>
                  ) : (
                    load.data.mine.map((r) => {
                      const why = load.data!.refused.get(r.name);
                      return (
                        <tr key={r.name}>
                          <td>
                            <a className="title-link" href={`#/requests/case/${idOf(r.name)}`}>
                              {datesLabel(r)}
                            </a>
                          </td>
                          <td className="nums">{daysOf(r)}</td>
                          <td>{leaveTypeLabel(r)}</td>
                          <td>{reasonOf(r)}</td>
                          <td>
                            <StatePill category={r.category} label={requestStatus(r)} />
                          </td>
                          <td>{why ? <span className="muted">{denialSentence(why, { viewer: s.me, requester: s.me })}</span> : null}</td>
                        </tr>
                      );
                    })
                  )}
                </tbody>
              </table>
            </div>
          )}
          {load.data?.more && <p className="muted">Only the newest requests were read: older ones are not shown.</p>}
        </section>
      </div>
    </RequestsFrame>
  );
}
