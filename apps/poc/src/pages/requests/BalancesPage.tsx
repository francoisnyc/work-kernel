// Balances, for HR (hr_admin): every balance HR can read, with the days
// waiting for approval per person. Read-only: opening a balance or
// changing an allowance stays in the generic UI and pli (Later).
import { useState, type ReactElement } from "react";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { seesBalances } from "../../model/devusers";
import { allBalancesSearch, balanceOf, pendingDays, requesterOf } from "../../model/leave";
import { personName } from "../../model/people";
import { Empty, Loading, PageHeader } from "../../ui/Page";
import { RequestsFrame } from "./RequestsRoutes";

export function BalancesPage(): ReactElement {
  const s = useSession();
  const [state, setState] = useState<"open" | "closed">("open");
  const load = useLoad(async () => {
    const [balances, waiting] = await Promise.all([
      s.api.searchItems({ ...allBalancesSearch(s.ns, state), ...s.readYourWrites }),
      s.api.searchItems({ parent: s.ns, filter: { types: [`${s.ns}/types/vacation_request`], states: ["submitted"] }, pageSize: 500, ...s.readYourWrites }),
    ]);
    const sorted = balances.items.map(balanceOf).sort((a, b) => (a.owner < b.owner ? -1 : a.owner > b.owner ? 1 : 0));
    return { balances: sorted, waiting: waiting.items, more: balances.nextPageToken !== "" };
  }, [state]);

  if (!seesBalances(s.user))
    return (
      <RequestsFrame current="balances">
        <PageHeader title="Leave balances" eyebrow="Requests" />
        <Empty title="For HR only">Balances across people are for those who hold hr_admin.</Empty>
      </RequestsFrame>
    );

  const caption = state === "open" ? "Open leave balances" : "Closed leave balances";
  return (
    <RequestsFrame current="balances">
      <PageHeader title="Leave balances" eyebrow="Requests · HR" load={load}>
        <p className="muted">Every balance you can read. Days waiting for approval are not taken until a request is approved.</p>
      </PageHeader>
      <div className="filters">
        <label htmlFor="balances-state" className="label">
          Show
        </label>
        <select id="balances-state" value={state} onChange={(e) => setState(e.target.value as "open" | "closed")}>
          <option value="open">Open balances</option>
          <option value="closed">Closed balances</option>
        </select>
      </div>
      <Loading load={load}>
        {(d) =>
          d.balances.length === 0 ? (
            <Empty title={state === "open" ? "No open balance" : "No closed balance"} />
          ) : (
            <div className="table-scroll">
              <table className="table">
                <caption className="sr-only">{caption}</caption>
                <thead>
                  <tr>
                    <th scope="col">Person</th>
                    <th scope="col">Allowance</th>
                    <th scope="col">Used</th>
                    <th scope="col">Left</th>
                    <th scope="col">Waiting for approval</th>
                  </tr>
                </thead>
                <tbody>
                  {d.balances.map((b) => (
                    <tr key={b.name}>
                      <td>{personName(b.owner)}</td>
                      <td className="nums">{b.allowance}</td>
                      <td className="nums">{b.used}</td>
                      <td className="nums">{b.left}</td>
                      <td className="nums">{state === "open" ? pendingDays(d.waiting.filter((r) => requesterOf(r) === b.owner)) : "—"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )
        }
      </Loading>
      {load.data?.more && <p className="muted">Only the first 500 balances are shown.</p>}
    </RequestsFrame>
  );
}
