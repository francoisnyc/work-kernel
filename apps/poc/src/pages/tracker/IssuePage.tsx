// An issue: its summary and description edited in place, its fields beside
// it (each saved on change, under the etag), its moves as buttons, its
// links both ways, comments and history.
import { LinkDirection, type Item, type ItemEvent, type Type } from "@gen/kernel/v1/kernel_pb";
import { useEffect, useState, type ReactElement } from "react";
import { idOf } from "../../app/router";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { shortInstant } from "../../model/countdown";
import { messagesOf } from "../../model/conversation";
import { fieldLabel, issueKey, optionLabel, stateLabel, typeIdOf } from "../../model/labels";
import { movesFrom } from "../../model/moves";
import { personName } from "../../model/people";
import { Fact } from "../../ui/Facts";
import { History } from "../../ui/History";
import { Icon } from "../../ui/Icon";
import { LinkDialog } from "../../ui/LinkDialog";
import { MessageView } from "../../ui/Messages";
import { MoveButtons } from "../../ui/MoveButtons";
import { Loading, PageHeader, StatePill } from "../../ui/Page";
import { RefusalNotice } from "../../ui/RefusalNotice";
import { TrackerFrame, spaceKey, useSpace } from "./TrackerRoutes";

type Data = { item: Item; type: Type; comments: Item[]; events: ItemEvent[]; others: Map<string, Item | undefined> };

// The links a person sets on an issue, both ways, as the page groups them.
const LINKS: Record<string, string> = {
  blocks: "Blocks",
  blocked_by: "Blocked by",
  relates: "Relates to",
  related_by: "Related to",
  duplicates: "Duplicates",
  duplicated_by: "Duplicated by",
};
const ADDABLE = [
  { value: "blocks", label: "Blocks" },
  { value: "relates", label: "Relates to" },
  { value: "duplicates", label: "Duplicates" },
];

export function IssuePage({ spaceId, id }: { spaceId: string; id: string }): ReactElement {
  const s = useSession();
  const space = useSpace(spaceId);
  const name = `${s.ns}/items/${id}`;
  const [editing, setEditing] = useState(false);
  const load = useLoad<Data>(
    async () => {
      const item = (await s.api.getItem({ name })).item!;
      const [type, comments, events] = await Promise.all([
        s.type(item.type),
        s.api.listItems({ parent: name, filter: { types: s.typeFilter("comment") }, pageSize: 500 }),
        s.api.listItemEvents({ parent: name, pageSize: 500 }),
      ]);
      const names = new Set<string>();
      for (const l of item.links) if (!l.item?.restricted && l.item?.name) names.add(l.item.name);
      for (const e of events.events) if (e.linkTarget?.name) names.add(e.linkTarget.name);
      const others = new Map(await Promise.all([...names].map(async (n) => [n, await s.item(n, true)] as const)));
      return { item, type, comments: comments.items, events: events.events, others };
    },
    [id],
    { paused: editing },
  );
  const [written, setWritten] = useState<Item>();
  useEffect(() => setWritten(undefined), [load.data]);
  const [error, setError] = useState<unknown>();
  const [linking, setLinking] = useState(false);

  return (
    <TrackerFrame spaceId={spaceId} space={space}>
      <Loading load={load}>
        {(d) => {
          const item = written ?? d.item;
          const f = item.fields ?? {};
          const typeId = typeIdOf(item.type);
          const declared = new Set(d.type.fields.map((x) => x.id));
          const options = Object.fromEntries(d.type.fields.filter((x) => x.options.length > 0).map((x) => [x.id, x.options]));
          const title = (n: string) => String(d.others.get(n)?.fields?.f_summary ?? "an issue");

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
            write(() => s.api.updateItem({ name, requestId: crypto.randomUUID(), etag: item.etag, fields: fields as never }));
          const membership = (link: string) => item.links.find((l) => l.linkType === link && l.direction === LinkDirection.OUTGOING);
          const others = item.links.filter((l) => LINKS[l.linkType]);
          const grouped = Object.keys(LINKS)
            .map((k) => [k, others.filter((l) => l.linkType === k)] as const)
            .filter(([, ls]) => ls.length > 0);

          return (
            <>
              <PageHeader
                eyebrow={issueKey(spaceKey(space), item.name)}
                title={<Summary text={String(f.f_summary ?? "")} onEditing={setEditing} onSave={(v) => update({ f_summary: v })} />}
                load={load}
              />
              <RefusalNotice error={error} ctx={{ typeId }} />
              <div className="detail" style={{ marginTop: 12 }}>
                <div className="stack">
                  <Description text={typeof f.f_description === "string" ? f.f_description : ""} onEditing={setEditing} onSave={(v) => update({ f_description: v === "" ? null : v })} />
                  <section className="section" aria-labelledby="links-title">
                    <div className="section-title">
                      <h2 id="links-title">Links</h2>
                      <button type="button" className="button button-small" onClick={() => setLinking(true)}>
                        <Icon name="link" />
                        Add link
                      </button>
                    </div>
                    {grouped.length === 0 ? (
                      <p className="muted">No links yet.</p>
                    ) : (
                      grouped.map(([k, ls]) => (
                        <div key={k}>
                          <h3>{LINKS[k]}</h3>
                          <ul className="plain rows">
                            {ls.map((l) => {
                              const other = l.item?.restricted ? undefined : d.others.get(l.item!.name);
                              return (
                                <li key={`${k}-${l.item?.name}`} className="row">
                                  <div className="row-main">
                                    {other ? <a href={`#/tracker/${spaceId}/issue/${idOf(other.name)}`}>{String(other.fields?.f_summary ?? idOf(other.name))}</a> : <span className="muted">An issue you can't see</span>}
                                  </div>
                                  {other && <StatePill category={other.category} label={stateLabel(typeIdOf(other.type), other.state)} />}
                                  {other && l.direction === LinkDirection.OUTGOING && (
                                    <button
                                      type="button"
                                      className="button button-quiet button-small"
                                      aria-label={`Remove link: ${LINKS[k]} ${String(other.fields?.f_summary ?? "")}`}
                                      onClick={() => void write(() => s.api.unlinkItems({ name, requestId: crypto.randomUUID(), etag: item.etag, linkType: k, target: other.name })).catch(() => {})}
                                    >
                                      <Icon name="x" />
                                    </button>
                                  )}
                                </li>
                              );
                            })}
                          </ul>
                        </div>
                      ))
                    )}
                  </section>
                  <section className="section" aria-labelledby="comments-title">
                    <h2 id="comments-title">Comments</h2>
                    <div className="thread">
                      {messagesOf(d.comments).map((m) => (
                        <MessageView key={m.name} m={m} viewer={s.me} audience="staff" />
                      ))}
                    </div>
                    <CommentBox
                      onSend={async (body) => {
                        await s.api.createItem({ parent: s.ns, requestId: crypto.randomUUID(), type: s.typeName("comment", 2), container: name, fields: { f_body: body } });
                        load.reload();
                      }}
                    />
                  </section>
                  <History events={d.events} typeId={typeId} audience="staff" titleOf={(ref) => (ref.restricted ? "a hidden item" : title(ref.name))} />
                </div>
                <aside className="card stack" aria-label="Details">
                  <dl className="facts">
                    <Fact term="Status">
                      <StatePill category={item.category} label={stateLabel(typeId, item.state)} />
                    </Fact>
                    <Fact term="Type">{typeId}</Fact>
                    <Fact term="Assignee" labelFor="issue-assignee">
                      <select id="issue-assignee" value={typeof f.f_assignee === "string" ? f.f_assignee : ""} onChange={(e) => void update({ f_assignee: e.target.value || null }).catch(() => {})}>
                        <option value="">Unassigned</option>
                        {withCurrent(s.staff().map((u) => u.principal), f.f_assignee).map((p) => (
                          <option key={p} value={p}>
                            {personName(p)}
                          </option>
                        ))}
                      </select>
                    </Fact>
                    <Fact term="Priority" labelFor="issue-priority">
                      <select id="issue-priority" value={typeof f.f_priority === "string" ? f.f_priority : ""} onChange={(e) => void update({ f_priority: e.target.value || null }).catch(() => {})}>
                        <option value="">None</option>
                        {(options.f_priority ?? []).map((o) => (
                          <option key={o} value={o}>
                            {o}
                          </option>
                        ))}
                      </select>
                    </Fact>
                    <Fact term="Points" labelFor="issue-points">
                      <Points value={typeof f.f_points === "string" ? f.f_points : ""} onSave={(v) => update({ f_points: v === "" ? null : v })} />
                    </Fact>
                    {declared.has("f_labels") && (
                      <Fact term="Labels" labelFor="issue-labels">
                        <Labels value={Array.isArray(f.f_labels) ? f.f_labels.map(String) : []} onChange={(ops) => write(() => s.api.updateItem({ name, requestId: crypto.randomUUID(), fields: { f_labels: ops } as never }))} />
                      </Fact>
                    )}
                    <Fact term="Epic">{membership("epic") ? title(membership("epic")!.item!.name) : <span className="muted">None</span>}</Fact>
                    <Fact term="Sprint">{membership("sprint") ? String(d.others.get(membership("sprint")!.item!.name)?.fields?.f_name ?? "A sprint") : <span className="muted">Backlog</span>}</Fact>
                    {typeof f.f_resolution === "string" && <Fact term="Resolution">{optionLabel("f_resolution", f.f_resolution)}</Fact>}
                    {typeof f.f_resolved_at === "string" && <Fact term={fieldLabel("f_resolved_at")}>{shortInstant(f.f_resolved_at)}</Fact>}
                  </dl>
                  <MoveButtons
                    moves={movesFrom(d.type, item.state, "staff")}
                    options={options}
                    ctx={{ typeId }}
                    onMove={(to, fields) => write(() => s.api.transitionItem({ name, requestId: crypto.randomUUID(), etag: item.etag, toState: to, fields: fields as never }))}
                  />
                </aside>
              </div>
              {linking && (
                <LinkDialog
                  kinds={ADDABLE}
                  find={async (words) =>
                    (await s.api.searchItems({ parent: s.ns, query: words, filter: { types: s.typeFilter("story", "bug", "task") }, pageSize: 10 })).items.filter((i) => i.name !== name)
                  }
                  describe={(it) => `${String(it.fields?.f_summary ?? idOf(it.name))} (${issueKey(spaceKey(space), it.name)})`}
                  onAdd={(kind, target) => write(() => s.api.linkItems({ name, requestId: crypto.randomUUID(), etag: item.etag, linkType: kind, target }))}
                  onClose={() => setLinking(false)}
                />
              )}
            </>
          );
        }}
      </Loading>
    </TrackerFrame>
  );
}

function withCurrent(list: string[], current: unknown): string[] {
  return typeof current === "string" && !list.includes(current) ? [...list, current] : list;
}

function Summary({ text, onSave, onEditing }: { text: string; onSave: (v: string) => Promise<void>; onEditing: (b: boolean) => void }): ReactElement {
  const [draft, setDraft] = useState<string>();
  const set = (v: string | undefined) => {
    setDraft(v);
    onEditing(v !== undefined);
  };
  if (draft === undefined) {
    return (
      <span className="inline-title">
        {text}
        <button type="button" className="button button-quiet button-small" onClick={() => set(text)}>
          Edit summary
        </button>
      </span>
    );
  }
  return (
    <form
      className="inline-title"
      onSubmit={async (e) => {
        e.preventDefault();
        if (draft.trim() === "") return;
        try {
          await onSave(draft.trim());
          set(undefined);
        } catch {
          // The page shows the refusal; the draft stays.
        }
      }}
    >
      <label htmlFor="issue-summary" className="sr-only">
        Summary
      </label>
      <input id="issue-summary" value={draft} autoFocus onChange={(e) => setDraft(e.target.value)} onKeyDown={(e) => e.key === "Escape" && set(undefined)} />
      <button type="submit" className="button button-primary button-small">
        Save
      </button>
      <button type="button" className="button button-small" onClick={() => set(undefined)}>
        Cancel
      </button>
    </form>
  );
}

function Description({ text, onSave, onEditing }: { text: string; onSave: (v: string) => Promise<void>; onEditing: (b: boolean) => void }): ReactElement {
  const [draft, setDraft] = useState<string>();
  const set = (v: string | undefined) => {
    setDraft(v);
    onEditing(v !== undefined);
  };
  return (
    <section className="section" aria-labelledby="description-title">
      <div className="section-title">
        <h2 id="description-title">Description</h2>
        {draft === undefined && (
          <button type="button" className="button button-quiet button-small" onClick={() => set(text)}>
            Edit description
          </button>
        )}
      </div>
      {draft === undefined ? (
        <p className={text === "" ? "muted" : "message-body"}>{text === "" ? "No description." : text}</p>
      ) : (
        <form
          className="stack"
          onSubmit={async (e) => {
            e.preventDefault();
            try {
              await onSave(draft.trim());
              set(undefined);
            } catch {
              // The page shows the refusal; the draft stays.
            }
          }}
        >
          <label htmlFor="issue-description" className="sr-only">
            Description
          </label>
          <textarea id="issue-description" rows={6} value={draft} autoFocus onChange={(e) => setDraft(e.target.value)} />
          <div className="button-row">
            <button type="submit" className="button button-primary">
              Save
            </button>
            <button type="button" className="button" onClick={() => set(undefined)}>
              Cancel
            </button>
          </div>
        </form>
      )}
    </section>
  );
}

function Points({ value, onSave }: { value: string; onSave: (v: string) => Promise<void> }): ReactElement {
  const [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);
  return (
    <input
      id="issue-points"
      inputMode="numeric"
      size={4}
      value={draft}
      onChange={(e) => setDraft(e.target.value.replace(/[^0-9]/g, ""))}
      onBlur={() => draft !== value && void onSave(draft).catch(() => setDraft(value))}
    />
  );
}

// Labels: a set. Adding or removing one sends a set operation, without an
// etag: two people adding labels at once both land.
function Labels({ value, onChange }: { value: string[]; onChange: (ops: { add?: string[]; remove?: string[] }) => Promise<void> }): ReactElement {
  const [draft, setDraft] = useState("");
  return (
    <div className="stack" style={{ gap: 6 }}>
      {value.length > 0 && (
        <ul className="plain button-row">
          {value.map((l) => (
            <li key={l} className="pill tone-neutral">
              {l}{" "}
              <button type="button" className="button-quiet" style={{ border: 0, background: "none", cursor: "pointer", color: "inherit" }} aria-label={`Remove label ${l}`} onClick={() => void onChange({ remove: [l] }).catch(() => {})}>
                ×
              </button>
            </li>
          ))}
        </ul>
      )}
      <form
        className="button-row"
        onSubmit={(e) => {
          e.preventDefault();
          const l = draft.trim();
          if (l === "") return;
          void onChange({ add: [l] }).then(() => setDraft(""), () => {});
        }}
      >
        <input id="issue-labels" value={draft} placeholder="Add a label" size={10} onChange={(e) => setDraft(e.target.value)} />
      </form>
    </div>
  );
}

function CommentBox({ onSend }: { onSend: (body: string) => Promise<void> }): ReactElement {
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>();
  return (
    <form
      className="card stack"
      onSubmit={async (e) => {
        e.preventDefault();
        if (draft.trim() === "") return;
        setBusy(true);
        setError(undefined);
        try {
          await onSend(draft.trim());
          setDraft("");
        } catch (err) {
          setError(err);
        } finally {
          setBusy(false);
        }
      }}
    >
      <label htmlFor="issue-comment" className="label">
        Add a comment
      </label>
      <textarea id="issue-comment" rows={3} value={draft} onChange={(e) => setDraft(e.target.value)} />
      <RefusalNotice error={error} />
      <div className="button-row">
        <button type="submit" className="button button-primary" disabled={busy || draft.trim() === ""}>
          Comment
        </button>
      </div>
    </form>
  );
}
