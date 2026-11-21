// A space's epics with their progress (the rollup, over what the reader
// can see), and one epic: its issues and its moves.
import { LinkDirection, type Item, type Type } from "@gen/kernel/v1/kernel_pb";
import type { ReactElement } from "react";
import { idOf } from "../../app/router";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { progress, progressLabel } from "../../model/epic";
import { issueKey, stateLabel, typeIdOf } from "../../model/labels";
import { movesFrom } from "../../model/moves";
import { Fact } from "../../ui/Facts";
import { MoveButtons } from "../../ui/MoveButtons";
import { Empty, Loading, PageHeader, StatePill } from "../../ui/Page";
import { TrackerFrame, spaceKey, useSpace } from "./TrackerRoutes";

const GUARD = "An epic is done only when all its issues are done or canceled.";

function ProgressBar({ item }: { item: Item }): ReactElement {
  const p = progress(item.rollups?.progress?.counts);
  const summary = String(item.fields?.f_summary ?? "Epic");
  return (
    <span className="button-row" style={{ flexWrap: "nowrap" }}>
      <span className="progress" role="progressbar" aria-label={`${summary} progress`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={p.percent}>
        <span style={{ width: `${p.percent}%` }} />
      </span>
      <span className="nums">{progressLabel(p)}</span>
    </span>
  );
}

export function EpicsPage({ spaceId }: { spaceId: string }): ReactElement {
  const s = useSession();
  const space = useSpace(spaceId);
  const load = useLoad(async () => {
    const res = await s.api.listItems({ parent: `${s.ns}/items/${spaceId}`, filter: { types: s.typeFilter("epic") }, pageSize: 100 });
    // Rollups come with GetItem only: one read per epic.
    return Promise.all(res.items.map(async (e) => (await s.api.getItem({ name: e.name })).item!));
  }, [spaceId]);
  return (
    <TrackerFrame spaceId={spaceId} space={space} current="epics">
      <PageHeader title="Epics" eyebrow={String(space?.fields?.f_name ?? "")} load={load}>
        <p className="muted">Progress counts the issues you can see, canceled ones left out.</p>
      </PageHeader>
      <Loading load={load}>
        {(epics) =>
          epics.length === 0 ? (
            <Empty title="No epics yet" />
          ) : (
            <ul className="plain rows">
              {epics.map((e) => (
                <li key={e.name} className="row">
                  <div className="row-main">
                    <a className="title-link" href={`#/tracker/${spaceId}/epic/${idOf(e.name)}`}>
                      {String(e.fields?.f_summary ?? idOf(e.name))}
                    </a>
                  </div>
                  <ProgressBar item={e} />
                  <StatePill category={e.category} label={stateLabel("epic", e.state)} />
                </li>
              ))}
            </ul>
          )
        }
      </Loading>
    </TrackerFrame>
  );
}

export function EpicPage({ spaceId, id }: { spaceId: string; id: string }): ReactElement {
  const s = useSession();
  const space = useSpace(spaceId);
  const name = `${s.ns}/items/${id}`;
  const load = useLoad<{ item: Item; type: Type; issues: Item[] }>(async () => {
    const item = (await s.api.getItem({ name })).item!;
    const [type, issues] = await Promise.all([
      s.type(item.type),
      s.api.searchItems({ parent: s.ns, orderBy: "f_rank", pageSize: 500, filter: { links: [{ linkType: "epic", direction: LinkDirection.OUTGOING, item: name }] }, ...s.readYourWrites }),
    ]);
    return { item, type, issues: issues.items };
  }, [id]);
  return (
    <TrackerFrame spaceId={spaceId} space={space} current="epics">
      <Loading load={load}>
        {(d) => (
          <>
            <PageHeader title={String(d.item.fields?.f_summary ?? "Epic")} eyebrow="Epic" load={load} />
            <div className="detail">
              <section className="section" aria-labelledby="epic-issues">
                <h2 id="epic-issues">Issues</h2>
                {d.issues.length === 0 ? (
                  <Empty title="No issues in this epic" />
                ) : (
                  <ul className="plain rows">
                    {d.issues.map((i) => (
                      <li key={i.name} className="row">
                        <span className="mono">{issueKey(spaceKey(space), i.name)}</span>
                        <div className="row-main">
                          <a href={`#/tracker/${spaceId}/issue/${idOf(i.name)}`}>{String(i.fields?.f_summary ?? idOf(i.name))}</a>
                        </div>
                        <StatePill category={i.category} label={stateLabel(typeIdOf(i.type), i.state)} />
                      </li>
                    ))}
                  </ul>
                )}
              </section>
              <aside className="card stack" aria-label="Details">
                <dl className="facts">
                  <Fact term="Status">
                    <StatePill category={d.item.category} label={stateLabel("epic", d.item.state)} />
                  </Fact>
                  <Fact term="Progress">
                    <ProgressBar item={d.item} />
                  </Fact>
                </dl>
                <MoveButtons
                  moves={movesFrom(d.type, d.item.state, "staff")}
                  options={{}}
                  ctx={{ typeId: "epic", guard: GUARD }}
                  onMove={async (to) => {
                    s.wrote(await s.api.transitionItem({ name, requestId: crypto.randomUUID(), etag: d.item.etag, toState: to }));
                    load.reload();
                  }}
                />
              </aside>
            </div>
          </>
        )}
      </Loading>
    </TrackerFrame>
  );
}
