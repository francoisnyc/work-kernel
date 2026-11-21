// "How can we help?": the desks a customer sees (portals).
import type { ReactElement } from "react";
import { idOf } from "../../app/router";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { Empty, Loading, PageHeader } from "../../ui/Page";
import { PortalFrame } from "./PortalRoutes";

export function PortalHome(): ReactElement {
  const s = useSession();
  const load = useLoad(async () => (await s.api.searchItems({ parent: s.ns, filter: { types: s.typeFilter("desk") }, pageSize: 100 })).items, []);
  return (
    <PortalFrame current="home">
      <PageHeader title="How can we help?" load={load}>
        <p className="muted">Choose where to send your request. You can follow it in My requests.</p>
      </PageHeader>
      <Loading load={load}>
        {(desks) =>
          desks.length === 0 ? (
            <Empty title="No help desk is open to you yet" />
          ) : (
            <ul className="plain desk-grid">
              {desks.map((d) => (
                <li key={d.name}>
                  <a className="desk-card" href={`#/portal/new/${idOf(d.name)}`}>
                    <span className="desk-card-name">{String(d.fields?.f_name ?? "Help desk")}</span>
                    <span className="muted">Open a request</span>
                  </a>
                </li>
              ))}
            </ul>
          )
        }
      </Loading>
    </PortalFrame>
  );
}
