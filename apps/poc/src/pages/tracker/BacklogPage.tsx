// A space's backlog: its open sprints (each in rank order), then the open
// issues in no open sprint. Quick filters, a word search held to the space,
// and a quick create at the bottom of the backlog.
import { LinkDirection, type Item } from "@gen/kernel/v1/kernel_pb";
import { useState, type ReactElement } from "react";
import { rankMove } from "../../app/rankmove";
import { idOf } from "../../app/router";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { backlogOnly, inContainer, issueFilter, noFilters, type IssueType, type QuickFilters } from "../../model/backlog";
import { issueKey, stateLabel, typeIdOf } from "../../model/labels";
import { personName } from "../../model/people";
import { Avatar } from "../../ui/Avatar";
import { Icon } from "../../ui/Icon";
import { Empty, Loading, PageHeader, StatePill } from "../../ui/Page";
import { RefusalNotice } from "../../ui/RefusalNotice";
import { TrackerFrame, spaceKey, useSpace } from "./TrackerRoutes";

type Section = { sprint: Item; issues: Item[] };
type Data = { sections: Section[]; backlog: Item[]; more: boolean; results?: Item[] };

const TYPES: { id: IssueType; label: string }[] = [
  { id: "story", label: "Stories" },
  { id: "bug", label: "Bugs" },
  { id: "task", label: "Tasks" },
];

export function BacklogPage({ spaceId }: { spaceId: string }): ReactElement {
  const s = useSession();
  const space = useSpace(spaceId);
  const spaceName = `${s.ns}/items/${spaceId}`;
  const [quick, setQuick] = useState<QuickFilters>(noFilters);
  const [words, setWords] = useState("");
  const [typed, setTyped] = useState("");
  const [pages, setPages] = useState(1);

  const load = useLoad<Data>(async () => {
    const filter = issueFilter(s.ns, quick);
    if (words !== "") {
      // No search within a container: search the namespace, keep this space.
      const res = await s.api.searchItems({ parent: s.ns, query: words, filter, pageSize: 100, ...s.readYourWrites });
      return { sections: [], backlog: [], more: false, results: inContainer(res.items, spaceName) };
    }
    const sprints = await s.api.listItems({ parent: spaceName, filter: { types: s.typeFilter("sprint"), states: ["active", "planned"] }, pageSize: 20 });
    // A sprint shows all its issues, done ones included; the quick filters
    // still apply.
    const sprintFilter = { types: filter.types, fields: filter.fields };
    const sections = await Promise.all(
      [...sprints.items]
        .sort((a, b) => (a.state === b.state ? 0 : a.state === "active" ? -1 : 1))
        .map(async (sprint) => ({
          sprint,
          issues: (
            await s.api.searchItems({
              parent: s.ns,
              orderBy: "f_rank",
              pageSize: 500,
              filter: { ...sprintFilter, links: [{ linkType: "sprint", direction: LinkDirection.OUTGOING, item: sprint.name }] },
              ...s.readYourWrites,
            })
          ).items,
        })),
    );
    const held = new Set(sections.flatMap((x) => x.issues.map((i) => i.name)));
    let items: Item[] = [];
    let token = "";
    for (let p = 0; p < pages; p++) {
      const res = await s.api.listItems({ parent: spaceName, filter, orderBy: "f_rank", pageSize: 50, pageToken: token });
      items = [...items, ...res.items];
      token = res.nextPageToken;
      if (token === "") break;
    }
    return { sections, backlog: backlogOnly(items, held), more: token !== "" };
  }, [spaceId, quick, words, pages]);

  const row = (i: Item) => (
    <li key={i.name} className="row">
      <span className="mono">{issueKey(spaceKey(space), i.name)}</span>
      <div className="row-main">
        <a className="title-link" href={`#/tracker/${spaceId}/issue/${idOf(i.name)}`}>
          {String(i.fields?.f_summary ?? idOf(i.name))}
        </a>
      </div>
      <span className="muted">{typeIdOf(i.type)}</span>
      {typeof i.fields?.f_priority === "string" && <span className="pill tone-neutral">{i.fields.f_priority}</span>}
      {typeof i.fields?.f_assignee === "string" && (
        <span title={personName(i.fields.f_assignee)}>
          <Avatar principal={i.fields.f_assignee} size={20} />
        </span>
      )}
      <StatePill category={i.category} label={stateLabel(typeIdOf(i.type), i.state)} />
    </li>
  );

  return (
    <TrackerFrame spaceId={spaceId} space={space} current="backlog">
      <PageHeader title="Backlog" eyebrow={String(space?.fields?.f_name ?? "")} load={load} />
      <form
        role="search"
        className="filters"
        onSubmit={(e) => {
          e.preventDefault();
          setWords(typed.trim());
        }}
      >
        <label htmlFor="backlog-search" className="sr-only">
          Search issues
        </label>
        <input id="backlog-search" type="search" placeholder="Search issues" value={typed} onChange={(e) => {
          setTyped(e.target.value);
          if (e.target.value === "") setWords("");
        }} />
        <button type="submit" className="button">
          <Icon name="search" />
          Search
        </button>
        <button type="button" className="chip" aria-pressed={quick.mine} onClick={() => setQuick({ ...quick, mine: !quick.mine })}>
          Only my issues
        </button>
        {TYPES.map((t) => (
          <button
            key={t.id}
            type="button"
            className="chip"
            aria-pressed={quick.types.includes(t.id)}
            onClick={() => setQuick({ ...quick, types: quick.types.includes(t.id) ? quick.types.filter((x) => x !== t.id) : [...quick.types, t.id] })}
          >
            {t.label}
          </button>
        ))}
        <label htmlFor="backlog-priority" className="sr-only">
          Priority filter
        </label>
        <select id="backlog-priority" value={quick.priority} onChange={(e) => setQuick({ ...quick, priority: e.target.value })}>
          <option value="">Any priority</option>
          {["P1", "P2", "P3", "P4"].map((p) => (
            <option key={p} value={p}>
              {p}
            </option>
          ))}
        </select>
        <label htmlFor="backlog-label" className="sr-only">
          Label filter
        </label>
        <input id="backlog-label" placeholder="Label" size={10} value={quick.label} onChange={(e) => setQuick({ ...quick, label: e.target.value.trim() })} />
      </form>
      <Loading load={load}>
        {(d) =>
          d.results ? (
            <section className="section" aria-labelledby="results-title">
              <h2 id="results-title">Results for “{words}”</h2>
              {d.results.length === 0 ? <Empty title="No open issue matches" /> : <ul className="plain rows">{d.results.map(row)}</ul>}
            </section>
          ) : (
            <>
              {d.sections.map(({ sprint, issues }) => (
                <section key={sprint.name} className="section" aria-labelledby={`sprint-${idOf(sprint.name)}`}>
                  <div className="section-title">
                    <h2 id={`sprint-${idOf(sprint.name)}`}>{String(sprint.fields?.f_name ?? "Sprint")}</h2>
                    <span className="muted">
                      {stateLabel("sprint", sprint.state)} · {issues.length} {issues.length === 1 ? "issue" : "issues"}
                    </span>
                  </div>
                  {issues.length === 0 ? <Empty title="No issues in this sprint" /> : <ul className="plain rows">{issues.map(row)}</ul>}
                </section>
              ))}
              <section className="section" aria-labelledby="backlog-title">
                <h2 id="backlog-title">Backlog</h2>
                {d.backlog.length === 0 && !d.more ? (
                  <Empty title="Nothing waiting">Every open issue is in a sprint.</Empty>
                ) : (
                  <ul className="plain rows">{d.backlog.map(row)}</ul>
                )}
                {d.more && (
                  <p>
                    <button type="button" className="button" onClick={() => setPages((p) => p + 1)}>
                      Load more
                    </button>
                  </p>
                )}
                <QuickCreate
                  onCreate={async (summary, typeId) => {
                    const res = await s.api.createItem({ parent: s.ns, requestId: crypto.randomUUID(), type: s.typeName(typeId, 3), container: spaceName, fields: { f_summary: summary } });
                    s.wrote(res);
                    await rankMove(s, res.item!, "bottom");
                    load.reload();
                  }}
                />
              </section>
            </>
          )
        }
      </Loading>
    </TrackerFrame>
  );
}

function QuickCreate({ onCreate }: { onCreate: (summary: string, typeId: IssueType) => Promise<void> }): ReactElement {
  const [summary, setSummary] = useState("");
  const [typeId, setTypeId] = useState<IssueType>("story");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  return (
    <form
      className="card filters"
      style={{ marginBottom: 0 }}
      onSubmit={async (e) => {
        e.preventDefault();
        if (summary.trim() === "") return;
        setBusy(true);
        setError(undefined);
        try {
          await onCreate(summary.trim(), typeId);
          setSummary("");
        } catch (err) {
          setError(err);
        } finally {
          setBusy(false);
        }
      }}
    >
      <label htmlFor="new-issue-summary" className="sr-only">
        New issue summary
      </label>
      <input id="new-issue-summary" placeholder="What needs doing?" style={{ flex: 1, minWidth: 200 }} value={summary} onChange={(e) => setSummary(e.target.value)} />
      <label htmlFor="new-issue-type" className="sr-only">
        Issue type
      </label>
      <select id="new-issue-type" value={typeId} onChange={(e) => setTypeId(e.target.value as IssueType)}>
        <option value="story">Story</option>
        <option value="bug">Bug</option>
        <option value="task">Task</option>
      </select>
      <button type="submit" className="button button-primary" disabled={busy || summary.trim() === ""}>
        <Icon name="plus" />
        Create
      </button>
      <RefusalNotice error={error} />
    </form>
  );
}
