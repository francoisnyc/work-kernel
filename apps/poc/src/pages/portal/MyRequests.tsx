// A customer's requests: their own, open or closed, newest first.
import { StateCategory, type Item } from "@gen/kernel/v1/kernel_pb";
import { useState, type ReactElement } from "react";
import { idOf } from "../../app/router";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { stateLabel } from "../../model/labels";
import { when } from "../../ui/format";
import { Empty, Loading, PageHeader, StatePill } from "../../ui/Page";
import { PortalFrame } from "./PortalRoutes";

const OPEN = [StateCategory.TRIAGE, StateCategory.ACTIVE, StateCategory.WAITING];
const CLOSED = [StateCategory.DONE, StateCategory.CANCELED];

export function MyRequests(): ReactElement {
  const s = useSession();
  const [tab, setTab] = useState<"open" | "closed">("open");
  const load = useLoad(async () => {
    const res = await s.api.searchItems({
      parent: s.ns,
      filter: { types: s.typeFilter("incident"), categories: tab === "open" ? OPEN : CLOSED },
      pageSize: 100,
    });
    const desks = new Map<string, string>();
    await Promise.all(
      [...new Set(res.items.map((i) => i.container))].map(async (c) => {
        const d = await s.item(c);
        desks.set(c, String(d?.fields?.f_name ?? ""));
      }),
    );
    return { items: res.items, desks };
  }, [tab]);
  return (
    <PortalFrame current="requests">
      <PageHeader title="My requests" load={load} />
      <div className="tabs" role="group" aria-label="Which requests">
        {(["open", "closed"] as const).map((t) => (
          <a
            key={t}
            href="#/portal/requests"
            aria-current={tab === t ? "page" : undefined}
            onClick={(e) => {
              e.preventDefault();
              setTab(t);
            }}
          >
            {t === "open" ? "Open" : "Closed"}
          </a>
        ))}
      </div>
      <Loading load={load}>
        {({ items, desks }) =>
          items.length === 0 ? (
            <Empty title={tab === "open" ? "No open requests" : "No closed requests"}>
              <a href="#/portal">Open a request</a>
            </Empty>
          ) : (
            <div className="table-scroll">
              <table className="table">
                <caption className="sr-only">Your requests</caption>
                <thead>
                  <tr>
                    <th scope="col">Request</th>
                    <th scope="col">Desk</th>
                    <th scope="col">Status</th>
                    <th scope="col">Opened</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((i: Item) => (
                    <tr key={i.name}>
                      <td>
                        <a className="title-link" href={`#/portal/requests/${idOf(i.name)}`}>
                          {String(i.fields?.f_title ?? "Request")}
                        </a>
                      </td>
                      <td>{desks.get(i.container) ?? ""}</td>
                      <td>
                        <StatePill category={i.category} label={stateLabel("incident", i.state, "customer")} />
                      </td>
                      <td>{when(i.createTime)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )
        }
      </Loading>
    </PortalFrame>
  );
}
