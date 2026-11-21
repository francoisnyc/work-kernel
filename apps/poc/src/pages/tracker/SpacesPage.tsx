// The spaces a person can see, newest first.
import type { ReactElement } from "react";
import { idOf } from "../../app/router";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { Empty, Loading, PageHeader } from "../../ui/Page";
import { TrackerFrame } from "./TrackerRoutes";

export function SpacesPage(): ReactElement {
  const s = useSession();
  const load = useLoad(async () => (await s.api.searchItems({ parent: s.ns, filter: { types: s.typeFilter("space") }, pageSize: 100, ...s.readYourWrites })).items, []);
  return (
    <TrackerFrame>
      <PageHeader title="Spaces" eyebrow="Tracker" load={load} />
      <Loading load={load}>
        {(spaces) =>
          spaces.length === 0 ? (
            <Empty title="No spaces yet">A space holds a project's issues, sprints and epics.</Empty>
          ) : (
            <ul className="plain rows">
              {spaces.map((sp) => (
                <li key={sp.name} className="row">
                  <div className="row-main">
                    <a className="title-link" href={`#/tracker/${idOf(sp.name)}/backlog`}>
                      {String(sp.fields?.f_name ?? idOf(sp.name))}
                    </a>
                  </div>
                  <span className="mono">{String(sp.fields?.f_key ?? "")}</span>
                </li>
              ))}
            </ul>
          )
        }
      </Loading>
    </TrackerFrame>
  );
}
