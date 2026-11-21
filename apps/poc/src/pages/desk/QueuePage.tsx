// A desk's queue: its open incidents, soonest due first, with the
// countdowns the goals copy onto them and the breaches the goals record.
import type { Item } from "@gen/kernel/v1/kernel_pb";
import { useState, type ReactElement } from "react";
import { useNow } from "../../app/poll";
import { idOf } from "../../app/router";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { countdown } from "../../model/countdown";
import { stateLabel } from "../../model/labels";
import { personName } from "../../model/people";
import { countLabel, inView, queueRequest, queueViews, type QueueSort, type QueueView } from "../../model/queue";
import { breachSearch, breachedIndex, dueCell, type BreachedIndex, type Metric } from "../../model/sla";
import { Icon } from "../../ui/Icon";
import { Empty, Loading, PageHeader, StatePill } from "../../ui/Page";
import { DeskFrame } from "./DeskRoutes";

export function QueuePage({ deskId, view: viewParam }: { deskId: string; view: string }): ReactElement {
  const s = useSession();
  const view = (queueViews.some((v) => v.view === viewParam) ? viewParam : "open") as QueueView;
  const [sort, setSort] = useState<QueueSort>("resolution");
  const [pages, setPages] = useState(1);
  const deskName = `${s.ns}/items/${deskId}`;
  const now = useNow(30_000);

  const load = useLoad(async () => {
    const [desk, goals] = await Promise.all([s.item(deskName), s.api.searchItems({ ...breachSearch(s.ns), ...s.readYourWrites })]);
    let items: Item[] = [];
    let token = "";
    for (let p = 0; p < pages; p++) {
      const res = await s.api.listItems(queueRequest(deskName, s.ns, view, sort, token));
      items = [...items, ...res.items];
      token = res.nextPageToken;
      if (token === "") break;
    }
    return { desk, items, more: token !== "", breached: breachedIndex(goals.items) };
  }, [deskId, view, sort, pages]);

  const title = String(load.data?.desk?.fields?.f_name ?? "Queue");
  return (
    <DeskFrame desk={{ id: deskId, name: title }}>
      <PageHeader title={title} eyebrow="Service desk · queue" load={load}>
        <p className="muted">Due instants are on the wall clock: the desk's calendar decides how much working time that is.</p>
      </PageHeader>
      <nav aria-label="Queue views" className="tabs">
        {queueViews.map((v) => (
          <a key={v.view} href={`#/desk/q/${deskId}/${v.view}`} aria-current={v.view === view ? "page" : undefined} onClick={() => setPages(1)}>
            {v.label}
          </a>
        ))}
      </nav>
      <div className="filters">
        <label htmlFor="queue-sort" className="label">
          Sort by
        </label>
        <select id="queue-sort" value={sort} onChange={(e) => setSort(e.target.value as QueueSort)}>
          <option value="resolution">Resolution due</option>
          <option value="first_response">First response due</option>
        </select>
      </div>
      <Loading load={load}>
        {(d) => {
          const shown = inView(d.items, view, d.breached);
          return shown.length === 0 && !d.more ? (
            <Empty title="Nothing in this view">{view === "breached" ? "No open incident has missed a service level." : "New incidents land here."}</Empty>
          ) : (
            <>
              <p className="muted" style={{ marginBottom: 8 }}>
                {countLabel(shown.length, d.more)} {shown.length === 1 && !d.more ? "incident" : "incidents"}
              </p>
              <div className="table-scroll">
                <table className="table">
                  <caption className="sr-only">Incidents</caption>
                  <thead>
                    <tr>
                      <th scope="col">Priority</th>
                      <th scope="col">Title</th>
                      <th scope="col">Requester</th>
                      <th scope="col">Assignee</th>
                      <th scope="col">Status</th>
                      <th scope="col">First response due (wall clock)</th>
                      <th scope="col">Resolution due (wall clock)</th>
                    </tr>
                  </thead>
                  <tbody>
                    {shown.map((i) => (
                      <tr key={i.name}>
                        <td>
                          <span className="pill tone-neutral">{String(i.fields?.f_priority ?? "—")}</span>
                        </td>
                        <td>
                          <a className="title-link" href={`#/desk/incident/${idOf(i.name)}`}>
                            {String(i.fields?.f_title ?? idOf(i.name))}
                          </a>
                        </td>
                        <td>{personName(i.creatorOnBehalfOf || i.creator)}</td>
                        <td>{typeof i.fields?.f_assignee === "string" ? personName(i.fields.f_assignee) : "Unassigned"}</td>
                        <td>
                          <StatePill category={i.category} label={stateLabel("incident", i.state)} />
                        </td>
                        <td className="nums">
                          <DueCellView incident={i} metric="first_response" breached={d.breached} now={now} />
                        </td>
                        <td className="nums">
                          <DueCellView incident={i} metric="resolution" breached={d.breached} now={now} />
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {d.more && (
                <p style={{ marginTop: 12 }}>
                  <button type="button" className="button" onClick={() => setPages((p) => p + 1)}>
                    Load more
                  </button>
                </p>
              )}
            </>
          );
        }}
      </Loading>
    </DeskFrame>
  );
}

function DueCellView({ incident, metric, breached, now }: { incident: Item; metric: Metric; breached: BreachedIndex; now: Date }): ReactElement {
  const c = dueCell(incident, metric, breached, now);
  switch (c.kind) {
    case "breached":
      return (
        <span className="badge tone-danger">
          <Icon name="alert" />
          Breached
        </span>
      );
    case "overdue":
      return (
        <span className="badge tone-danger" title="The due instant has passed; the goal has not recorded the breach yet.">
          <Icon name="clock" />
          Overdue
        </span>
      );
    case "paused":
      return (
        <span className="badge tone-waiting">
          <Icon name="pause" />
          Paused
        </span>
      );
    case "countdown":
      return <span title={c.due}>{countdown(c.due, now)}</span>;
    default:
      return <span className="muted">—</span>;
  }
}
