// The desks an agent can work: desk@2, the version the app knows.
import type { ReactElement } from "react";
import { idOf } from "../../app/router";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { versionOf } from "../../model/labels";
import { Empty, Loading, PageHeader } from "../../ui/Page";
import { DeskFrame } from "./DeskRoutes";

export function DeskList(): ReactElement {
  const s = useSession();
  const load = useLoad(async () => {
    const res = await s.api.searchItems({ parent: s.ns, filter: { types: s.typeFilter("desk") }, pageSize: 100, ...s.readYourWrites });
    return res.items.filter((d) => versionOf(d.type) === 2);
  }, []);
  return (
    <DeskFrame>
      <PageHeader title="Service desk" load={load} />
      <Loading load={load}>
        {(desks) =>
          desks.length === 0 ? (
            <Empty title="No desks yet">An administrator creates a desk and links its calendar and service levels.</Empty>
          ) : (
            <ul className="plain rows">
              {desks.map((d) => (
                <li key={d.name} className="row">
                  <div className="row-main">
                    <a className="title-link" href={`#/desk/q/${idOf(d.name)}`}>
                      {String(d.fields?.f_name ?? idOf(d.name))}
                    </a>
                  </div>
                  <span className="mono">{String(d.fields?.f_key ?? "")}</span>
                </li>
              ))}
            </ul>
          )
        }
      </Loading>
    </DeskFrame>
  );
}
