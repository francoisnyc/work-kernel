// An incident, as an agent works it: its facts, moves, service levels as
// the goals record them, the conversation (replies to the customer and
// internal notes, never confused), the bugs that caused it, its history.
import { LinkDirection, type Item, type ItemEvent, type Type } from "@gen/kernel/v1/kernel_pb";
import { useEffect, useState, type ReactElement } from "react";
import { useNow } from "../../app/poll";
import { idOf } from "../../app/router";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { wallClock } from "../../model/countdown";
import { messagesOf } from "../../model/conversation";
import { optionLabel, stateLabel } from "../../model/labels";
import { movesFrom } from "../../model/moves";
import { personName } from "../../model/people";
import { postMessage } from "../../model/requests";
import { goalStatus, goalTimeline, metricLabel, type GoalEntry, type GoalStatus } from "../../model/sla";
import { Composer } from "../../ui/Composer";
import { Fact } from "../../ui/Facts";
import { isoOf, when } from "../../ui/format";
import { History } from "../../ui/History";
import { Icon } from "../../ui/Icon";
import { LinkDialog } from "../../ui/LinkDialog";
import { MessageView } from "../../ui/Messages";
import { MoveButtons } from "../../ui/MoveButtons";
import { Empty, Loading, PageHeader, StatePill } from "../../ui/Page";
import { RefusalNotice } from "../../ui/RefusalNotice";
import { DeskFrame } from "./DeskRoutes";

type Data = {
  item: Item;
  type: Type;
  desk?: Item;
  children: Item[];
  goalEvents: Map<string, ItemEvent[]>;
  events: ItemEvent[];
  causes: { name: string; item?: Item }[];
};

const METRICS = ["first_response", "resolution"] as const;

export function IncidentPage({ id }: { id: string }): ReactElement {
  const s = useSession();
  const name = `${s.ns}/items/${id}`;
  const load = useLoad<Data>(async () => {
    const item = (await s.api.getItem({ name })).item!;
    const [type, desk, children, events] = await Promise.all([
      s.type(item.type),
      item.container ? s.item(item.container) : Promise.resolve(undefined),
      s.api.listItems({ parent: name, filter: { types: s.typeFilter("reply", "note", "sla_goal") }, pageSize: 500 }),
      s.api.listItemEvents({ parent: name, pageSize: 500 }),
    ]);
    const goals = children.items.filter((c) => c.type.includes("/types/sla_goal/"));
    const goalEvents = new Map(
      await Promise.all(goals.map(async (g) => [g.name, (await s.api.listItemEvents({ parent: g.name, pageSize: 200 })).events] as const)),
    );
    const causes = await Promise.all(
      item.links
        .filter((l) => l.linkType === "caused_by" && l.direction === LinkDirection.OUTGOING && !l.item?.restricted)
        .map(async (l) => ({ name: l.item!.name, item: await s.item(l.item!.name, true) })),
    );
    return { item, type, desk, children: children.items, goalEvents, events: events.events, causes };
  }, [id]);

  // A write's answer is the newest Item: shown (and its etag used) until
  // the next read lands.
  const [written, setWritten] = useState<Item>();
  useEffect(() => setWritten(undefined), [load.data]);
  const [error, setError] = useState<unknown>();
  const [linking, setLinking] = useState(false);

  return (
    <DeskFrame desk={load.data?.desk ? { id: idOf(load.data.desk.name), name: String(load.data.desk.fields?.f_name ?? "Desk") } : undefined}>
      <Loading load={load}>
        {(d) => {
          const item = written ?? d.item;
          const f = item.fields ?? {};
          const title = String(f.f_title ?? idOf(item.name));
          const requester = item.creatorOnBehalfOf || item.creator;
          const options = Object.fromEntries(d.type.fields.filter((x) => x.options.length > 0).map((x) => [x.id, x.options]));

          async function write(run: () => Promise<{ item?: Item; commitSeq: bigint }>) {
            setError(undefined);
            try {
              const res = await run();
              s.wrote(res);
              if (res.item) setWritten(res.item);
              load.reload();
            } catch (e) {
              setError(e);
              throw e;
            }
          }
          const update = (fields: Record<string, unknown>) =>
            write(() => s.api.updateItem({ name, requestId: crypto.randomUUID(), etag: item.etag, fields: fields as never })).catch(() => {});

          return (
            <>
              <PageHeader title={title} eyebrow={`Incident · ${String(f.f_priority ?? "")}`} load={load} />
              <div className="detail">
                <div className="stack">
                  <section className="card stack" aria-label="Moves">
                    <MoveButtons
                      moves={movesFrom(d.type, item.state, "staff")}
                      options={options}
                      ctx={{ typeId: "incident" }}
                      onMove={(to, fields) =>
                        write(() =>
                          s.api.transitionItem({ name, requestId: crypto.randomUUID(), etag: item.etag, toState: to, fields: fields as never }),
                        )
                      }
                    />
                  </section>
                  <RefusalNotice error={error} ctx={{ typeId: "incident" }} />
                  <ServiceLevels children={d.children} goalEvents={d.goalEvents} />
                  <section className="section" aria-labelledby="conversation-title">
                    <h2 id="conversation-title">Conversation</h2>
                    <div className="thread">
                      {messagesOf(d.children).map((m) => (
                        <MessageView key={m.name} m={m} viewer={s.me} audience="staff" />
                      ))}
                    </div>
                    <Composer
                      requester={personName(requester)}
                      onSend={async (kind, body) => {
                        const it = await postMessage(s.api, s.ns, name, kind, body);
                        void it;
                        load.reload();
                      }}
                    />
                  </section>
                  <History
                    events={d.events}
                    typeId="incident"
                    audience="staff"
                    titleOf={(ref) => (ref.restricted ? "a hidden item" : d.causes.find((c) => c.name === ref.name)?.item?.fields?.f_summary?.toString() ?? "an item")}
                  />
                </div>
                <aside className="card" aria-label="Details">
                  <dl className="facts">
                    <Fact term="Status">
                      <StatePill category={item.category} label={stateLabel("incident", item.state)} />
                    </Fact>
                    <Fact term="Requester">{personName(requester)}</Fact>
                    <Fact term="Assignee" labelFor="incident-assignee">
                      <div className="stack" style={{ gap: 6 }}>
                        <select
                          id="incident-assignee"
                          value={typeof f.f_assignee === "string" ? f.f_assignee : ""}
                          onChange={(e) => void update({ f_assignee: e.target.value === "" ? null : e.target.value })}
                        >
                          <option value="">Unassigned</option>
                          {assignees(s.staff("desk_agent").map((u) => u.principal), f.f_assignee).map((p) => (
                            <option key={p} value={p}>
                              {personName(p)}
                            </option>
                          ))}
                        </select>
                        {f.f_assignee !== s.me && (
                          <button type="button" className="button button-small" onClick={() => void update({ f_assignee: s.me })}>
                            Assign to me
                          </button>
                        )}
                      </div>
                    </Fact>
                    {(["f_priority", "f_impact"] as const).map((fid) => (
                      <Fact key={fid} term={fid === "f_priority" ? "Priority" : "Impact"} labelFor={`incident-${fid}`}>
                        <select id={`incident-${fid}`} value={typeof f[fid] === "string" ? (f[fid] as string) : ""} onChange={(e) => void update({ [fid]: e.target.value === "" ? null : e.target.value })}>
                          {fid === "f_impact" && <option value="">Not set</option>}
                          {(options[fid] ?? []).map((o) => (
                            <option key={o} value={o}>
                              {optionLabel(fid, o)}
                            </option>
                          ))}
                        </select>
                      </Fact>
                    ))}
                    {typeof f.f_resolution === "string" && <Fact term="Resolution">{optionLabel("f_resolution", f.f_resolution)}</Fact>}
                    <Fact term="Opened">{when(item.createTime)}</Fact>
                  </dl>
                  <section className="section" aria-labelledby="causes-title" style={{ marginTop: 16 }}>
                    <div className="section-title">
                      <h2 id="causes-title">Caused by</h2>
                      <button type="button" className="button button-small" onClick={() => setLinking(true)}>
                        <Icon name="link" />
                        Link a bug
                      </button>
                    </div>
                    {d.causes.length === 0 ? (
                      <p className="muted">No bug linked.</p>
                    ) : (
                      <ul className="plain stack" style={{ gap: 6 }}>
                        {d.causes.map((c) => (
                          <li key={c.name}>
                            {c.item ? (
                              <>
                                {String(c.item.fields?.f_summary ?? idOf(c.name))}{" "}
                                <StatePill category={c.item.category} label={stateLabel("bug", c.item.state)} />
                              </>
                            ) : (
                              <span className="muted">A bug you can't see</span>
                            )}
                          </li>
                        ))}
                      </ul>
                    )}
                  </section>
                </aside>
              </div>
              {linking && (
                <LinkDialog
                  kinds={[{ value: "caused_by", label: "Caused by" }]}
                  find={async (words) => (await s.api.searchItems({ parent: s.ns, query: words, filter: { types: s.typeFilter("bug") }, pageSize: 10 })).items}
                  describe={(it) => String(it.fields?.f_summary ?? idOf(it.name))}
                  onAdd={(kind, target) => write(() => s.api.linkItems({ name, requestId: crypto.randomUUID(), etag: item.etag, linkType: kind, target }))}
                  onClose={() => setLinking(false)}
                />
              )}
            </>
          );
        }}
      </Loading>
    </DeskFrame>
  );
}

function assignees(agents: string[], current: unknown): string[] {
  return typeof current === "string" && !agents.includes(current) ? [...agents, current] : agents;
}

// The goals, newest cycle of each metric first, as they record themselves.
function ServiceLevels({ children, goalEvents }: { children: Item[]; goalEvents: Map<string, ItemEvent[]> }): ReactElement {
  const now = useNow(30_000);
  const goals = children
    .filter((c) => c.type.includes("/types/sla_goal/"))
    .sort((a, b) => (isoOf(a.createTime) < isoOf(b.createTime) ? -1 : 1));
  const latest = (metric: string) => goals.filter((g) => g.fields?.f_metric === metric).at(-1);
  return (
    <>
      <section className="section" aria-labelledby="levels-title">
        <h2 id="levels-title">Service levels</h2>
        {goals.length === 0 ? (
          <Empty title="No service levels">This desk has no policy, or its goals have not started yet.</Empty>
        ) : (
          <ul className="plain levels">
            {METRICS.map((m) => {
              const g = latest(m);
              return g ? (
                <li key={m} className="level">
                  <span className="level-name">{metricLabel(m)}</span>
                  <GoalStatusView status={goalStatus(g, now)} now={now} />
                </li>
              ) : null;
            })}
          </ul>
        )}
      </section>
      {goals.length > 0 && (
        <section className="section" aria-labelledby="timeline-title">
          <h2 id="timeline-title">Service level timeline</h2>
          {METRICS.map((m) => {
            const entries = goals.filter((g) => g.fields?.f_metric === m).flatMap((g) => goalTimeline(goalEvents.get(g.name) ?? []));
            return entries.length === 0 ? null : (
              <div key={m}>
                <h3>{metricLabel(m)}</h3>
                <ol className="plain timeline" aria-label={metricLabel(m)}>
                  {entries.map((e, i) => (
                    <li key={i}>
                      {entryWords(e)} · <time dateTime={e.at}>{when(e.at)}</time>
                    </li>
                  ))}
                </ol>
              </div>
            );
          })}
        </section>
      )}
    </>
  );
}

function entryWords(e: GoalEntry): string {
  const word = { started: "Started", paused: "Paused", resumed: "Resumed", breached: "Breached", met: "Met", missed: "Missed", completed: "Completed", canceled: "Canceled" }[e.kind];
  return e.due ? `${word} (due ${when(e.due)})` : word;
}

function GoalStatusView({ status, now }: { status: GoalStatus; now: Date }): ReactElement {
  switch (status.kind) {
    case "running":
      return <span className="nums">{status.due ? `Due ${wallClock(status.due, now)}` : "Running"}</span>;
    case "overdue":
      return (
        <span className="badge tone-danger">
          <Icon name="clock" />
          Overdue · was due {wallClock(status.due, now)}
        </span>
      );
    case "breached":
      return (
        <span className="badge tone-danger">
          <Icon name="alert" />
          Breached{status.paused ? " · paused" : ""}
        </span>
      );
    case "paused":
      return (
        <span className="badge tone-waiting">
          <Icon name="pause" />
          Paused
        </span>
      );
    case "met":
      return (
        <span className="badge tone-done">
          <Icon name="check" />
          Met
        </span>
      );
    case "missed":
      return (
        <span className="badge tone-danger">
          <Icon name="x" />
          Missed
        </span>
      );
    case "canceled":
      return <span className="badge tone-canceled">Canceled</span>;
    default:
      return <span className="badge tone-neutral">Completed</span>;
  }
}
