// The active sprint's board: a column per state, rank order inside each.
// Drag a card (or pick it up with Space and move it with the arrows) to
// rank it in its column, or into another column to move it there: the
// transition first, the rank write only once it succeeded. A move the Type
// does not declare is refused before anything is written.
import { LinkDirection, type Item, type Type } from "@gen/kernel/v1/kernel_pb";
import { useEffect, useRef, useState, type KeyboardEvent, type PointerEvent, type ReactElement } from "react";
import { useAnnounce } from "../../app/announce";
import { useNow } from "../../app/poll";
import { rankMove } from "../../app/rankmove";
import { idOf } from "../../app/router";
import { useSession } from "../../app/session";
import { useLoad } from "../../app/useLoad";
import { COLUMNS, boardKey, columnsOf, planDrop, runDrop, type BoardKey, type Card, type Column, type DropPlan, type Holding } from "../../model/board";
import { wallClock } from "../../model/countdown";
import { issueKey, stateLabel, transitionLabel, typeIdOf } from "../../model/labels";
import { declaresMove, movesFrom } from "../../model/moves";
import { personName } from "../../model/people";
import { Avatar } from "../../ui/Avatar";
import { Icon } from "../../ui/Icon";
import { MoveButtons, RequiredDialog } from "../../ui/MoveButtons";
import { Empty, Loading, PageHeader } from "../../ui/Page";
import { RefusalNotice } from "../../ui/RefusalNotice";
import { TrackerFrame, spaceKey, useSpace } from "./TrackerRoutes";

type Data = { sprint?: Item; sprintType?: Type; items: Item[]; types: Map<string, Type> };
type Target = { column: Column; index: number };
// What the board says after an action: a refusal, or a sentence.
type Notice = { error: unknown } | { text: string } | undefined;

const KEYS = new Set<string>([" ", "Enter", "Escape", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight"]);

export function BoardPage({ spaceId }: { spaceId: string }): ReactElement {
  const s = useSession();
  const space = useSpace(spaceId);
  const spaceName = `${s.ns}/items/${spaceId}`;
  const announce = useAnnounce();
  const now = useNow(30_000);
  const [mine, setMine] = useState(false);
  const [showCanceled, setShowCanceled] = useState(false);
  const [notice, setNotice] = useState<Notice>();
  const [asking, setAsking] = useState<{ card: string; plan: Extract<DropPlan, { kind: "transition" }>; required: string[]; options: Record<string, string[]>; label: string }>();
  const [holding, setHolding] = useState<Holding>();
  const [drag, setDrag] = useState<{ card: string; target?: Target }>();

  const load = useLoad<Data>(
    async () => {
      const sprints = await s.api.listItems({ parent: spaceName, filter: { types: s.typeFilter("sprint"), states: ["active"] }, pageSize: 10 });
      const sprint = sprints.items[0];
      if (!sprint) return { items: [], types: new Map() };
      const res = await s.api.searchItems({
        parent: s.ns,
        orderBy: "f_rank",
        pageSize: 500,
        filter: {
          links: [{ linkType: "sprint", direction: LinkDirection.OUTGOING, item: sprint.name }],
          ...(mine ? { fields: [{ field: "f_assignee", eq: { kind: { case: "stringValue", value: "me" } } }] } : {}),
        },
        ...s.readYourWrites,
      });
      const names = [...new Set(res.items.map((i) => i.type))];
      const types = new Map(await Promise.all(names.map(async (n) => [n, await s.type(n)] as const)));
      return { sprint, sprintType: await s.type(sprint.type), items: res.items, types };
    },
    [spaceId, mine],
    { paused: drag !== undefined || holding !== undefined || asking !== undefined },
  );

  const d = load.data;
  const items = d?.items ?? [];
  const byName = new Map(items.map((i) => [i.name, i]));
  const cards: Card[] = items.map((i) => ({ name: i.name, container: i.container, state: i.state, key: typeof i.fields?.f_rank === "string" ? i.fields.f_rank : "" }));
  const cols = columnsOf(cards);
  const titleOf = (name: string) => String(byName.get(name)?.fields?.f_summary ?? idOf(name));
  const declares = (card: string) => (from: string, to: string) => {
    const t = d?.types.get(byName.get(card)?.type ?? "");
    return t ? declaresMove(t, from, to) : false;
  };

  async function carryOut(card: string, plan: DropPlan, fields?: Record<string, string>) {
    const item = byName.get(card)!;
    setNotice(undefined);
    let rebalance = false;
    try {
      await runDrop(
        {
          transition: async (_c, to, f) => {
            s.wrote(await s.api.transitionItem({ name: card, requestId: crypto.randomUUID(), etag: item.etag, toState: to, fields: f as never }));
          },
          rank: async (_c, pos, other) => {
            const fresh = (await s.api.getItem({ name: card })).item!;
            rebalance = (await rankMove(s, fresh, pos, other)).needsRebalance;
          },
        },
        card,
        plan,
        fields,
      );
      announce(`${titleOf(card)} moved.`);
      if (rebalance) setNotice({ text: "Moved. This space's ranks have grown long: it needs a rebalance (pli rank rebalance)." });
    } catch (e) {
      setNotice({ error: e });
      announce(`${titleOf(card)} was not moved.`);
    }
    load.reload();
  }

  // A drop: refused before any write, asked for its required fields, or done.
  function drop(card: string, target: Target) {
    const plan = planDrop(cards, card, target.column, target.index, declares(card));
    const item = byName.get(card)!;
    if (plan.kind === "none") return;
    if (plan.kind === "refused") {
      const text =
        plan.reason === "no_transition"
          ? `An issue can't go from ${stateLabel(typeIdOf(item.type), item.state)} to ${stateLabel(typeIdOf(item.type), target.column)}.`
          : "That place is between issues of another space: drop it next to one of this space's issues.";
      setNotice({ text });
      announce(text);
      return;
    }
    if (plan.kind === "transition") {
      const type = d!.types.get(item.type)!;
      const t = type.transitions.find((x) => x.fromState === item.state && x.toState === plan.to);
      if (t && t.required.length > 0) {
        const options = Object.fromEntries(type.fields.filter((f) => f.options.length > 0).map((f) => [f.id, f.options]));
        setAsking({ card, plan, required: [...t.required], options, label: transitionLabel(type.typeId, item.state, plan.to) });
        return;
      }
    }
    void carryOut(card, plan);
  }

  // --- The pointer ---
  const start = useRef<{ card: string; x: number; y: number; moved: boolean } | undefined>(undefined);
  const suppressClick = useRef(false);

  function targetAt(x: number, y: number, card: string): Target | undefined {
    const el = document.elementFromPoint(x, y)?.closest<HTMLElement>("[data-column]");
    const column = el?.dataset.column as Column | undefined;
    if (!el || !column) return undefined;
    const others = [...el.querySelectorAll<HTMLElement>("[data-card]")].filter((c) => c.dataset.card !== card);
    const index = others.findIndex((c) => {
      const r = c.getBoundingClientRect();
      return y < r.top + r.height / 2;
    });
    return { column, index: index < 0 ? others.length : index };
  }

  useEffect(() => {
    function onMove(e: globalThis.PointerEvent) {
      const st = start.current;
      if (!st) return;
      if (!st.moved && Math.hypot(e.clientX - st.x, e.clientY - st.y) < 5) return;
      st.moved = true;
      setDrag({ card: st.card, target: targetAt(e.clientX, e.clientY, st.card) });
    }
    function onUp(e: globalThis.PointerEvent) {
      const st = start.current;
      start.current = undefined;
      if (!st?.moved) return;
      suppressClick.current = true;
      setTimeout(() => (suppressClick.current = false), 0);
      const target = targetAt(e.clientX, e.clientY, st.card);
      setDrag(undefined);
      if (target) drop(st.card, target);
    }
    document.addEventListener("pointermove", onMove);
    document.addEventListener("pointerup", onUp);
    return () => {
      document.removeEventListener("pointermove", onMove);
      document.removeEventListener("pointerup", onUp);
    };
  });

  function onPointerDown(e: PointerEvent, card: string) {
    if (e.button !== 0 || (e.target as HTMLElement).closest("button")) return;
    start.current = { card, x: e.clientX, y: e.clientY, moved: false };
  }

  // --- The keyboard ---
  const columnNames = Object.fromEntries(COLUMNS.map((c) => [c, cols[c].map((x) => x.name)])) as Record<Column, string[]>;
  function onMoveKey(e: KeyboardEvent, card: string) {
    if (!KEYS.has(e.key) || (!holding && e.key !== " " && e.key !== "Enter")) return;
    e.preventDefault();
    const r = boardKey(holding, e.key as BoardKey, card, columnNames);
    const title = titleOf(card);
    if (r.cancel) {
      setHolding(undefined);
      announce(`${title}: move canceled.`);
    } else if (r.drop) {
      setHolding(undefined);
      drop(card, { column: r.drop.column, index: r.drop.index });
    } else if (r.holding) {
      const h = r.holding;
      const count = columnNames[h.column].filter((c) => c !== card).length + 1;
      announce(
        holding
          ? `${title}: ${stateLabel("story", h.column)}, position ${h.index + 1} of ${count}.`
          : `Picked up ${title}. Arrow keys move it, Space drops it, Escape cancels.`,
      );
      setHolding(h);
    }
  }

  const target: Target | undefined = drag?.target ?? (holding ? { column: holding.column, index: holding.index } : undefined);
  const moving = drag?.card ?? holding?.card;

  const sprint = d?.sprint;
  const endAt = typeof sprint?.fields?.f_end_at === "string" ? sprint.fields.f_end_at : "";
  const endDate = typeof sprint?.fields?.f_end === "string" ? sprint.fields.f_end : "";

  return (
    <TrackerFrame spaceId={spaceId} space={space} current="board">
      <Loading load={load}>
        {() =>
          !sprint ? (
            <>
              <PageHeader title="Board" load={load} />
              <Empty title="No active sprint">Start a sprint from the backlog to see its board.</Empty>
            </>
          ) : (
            <>
              <PageHeader
                title={String(sprint.fields?.f_name ?? "Sprint")}
                eyebrow="Active sprint"
                load={load}
                actions={
                  d?.sprintType && (
                    <MoveButtons
                      moves={movesFrom(d.sprintType, sprint.state, "staff")}
                      options={{}}
                      ctx={{ typeId: "sprint" }}
                      onMove={async (to) => {
                        s.wrote(await s.api.transitionItem({ name: sprint.name, requestId: crypto.randomUUID(), etag: sprint.etag, toState: to }));
                        load.reload();
                      }}
                    />
                  )
                }
              >
                {typeof sprint.fields?.f_goal === "string" && <p>{sprint.fields.f_goal}</p>}
                <p className="muted">{endAt ? `Ends ${wallClock(endAt, now)}` : endDate ? `Ends on ${endDate}` : "No end set"}</p>
              </PageHeader>
              <div className="filters">
                <button type="button" className="chip" aria-pressed={mine} onClick={() => setMine(!mine)}>
                  Only my issues
                </button>
                {cols.canceled.length > 0 && (
                  <button type="button" className="chip" aria-pressed={showCanceled} onClick={() => setShowCanceled(!showCanceled)}>
                    {cols.canceled.length} canceled
                  </button>
                )}
                <span className="muted">Drag a card, or focus its handle and press Space, then the arrow keys.</span>
              </div>
              {notice && ("error" in notice ? <RefusalNotice error={notice.error} ctx={{ typeId: "story" }} /> : <p className="notice notice-warning" role="alert">{notice.text}</p>)}
              <div className="board" style={{ marginTop: 12 }}>
                {COLUMNS.map((col) => {
                  const label = stateLabel("story", col);
                  const list = cols[col];
                  const headId = `col-${col}`;
                  return (
                    <section key={col} className={`column${target?.column === col ? " column-target" : ""}`} data-column={col} aria-labelledby={headId}>
                      <h2 className="column-head" id={headId}>
                        {label}
                        <span className="muted" aria-hidden="true">
                          {list.length}
                        </span>
                      </h2>
                      <ul className="plain cards" aria-label={label}>
                        {list.map((c) => {
                          const it = byName.get(c.name)!;
                          const others = list.filter((x) => x.name !== moving);
                          const at = target?.column === col && moving !== c.name ? others.findIndex((x) => x.name === c.name) : -1;
                          const marker = at < 0 ? "" : at === target!.index ? " drop-before" : at === target!.index - 1 && target!.index === others.length ? " drop-after" : "";
                          return (
                            <li
                              key={c.name}
                              data-card={c.name}
                              className={`card-item${drag?.card === c.name ? " dragging" : ""}${holding?.card === c.name ? " held" : ""}${marker}`}
                              onPointerDown={(e) => onPointerDown(e, c.name)}
                              onClickCapture={(e) => {
                                if (suppressClick.current) {
                                  e.preventDefault();
                                  e.stopPropagation();
                                }
                              }}
                            >
                              <a className="card-title" href={`#/tracker/${spaceId}/issue/${idOf(c.name)}`} draggable={false}>
                                {String(it.fields?.f_summary ?? idOf(c.name))}
                              </a>
                              <span className="card-meta">
                                <span>{typeIdOf(it.type)}</span>
                                <span className="mono">{issueKey(spaceKey(space), c.name)}</span>
                                {typeof it.fields?.f_priority === "string" && <span className="pill tone-neutral">{it.fields.f_priority}</span>}
                                {typeof it.fields?.f_assignee === "string" && (
                                  <span title={personName(it.fields.f_assignee)}>
                                    <Avatar principal={it.fields.f_assignee} size={20} />
                                  </span>
                                )}
                              </span>
                              <button
                                type="button"
                                className="button button-quiet card-move"
                                aria-label={`Move ${String(it.fields?.f_summary ?? idOf(c.name))}`}
                                aria-pressed={holding?.card === c.name}
                                onKeyDown={(e) => onMoveKey(e, c.name)}
                                onClick={(e) => e.preventDefault()}
                                onBlur={() => holding?.card === c.name && setHolding(undefined)}
                              >
                                <Icon name="grip" />
                              </button>
                            </li>
                          );
                        })}
                      </ul>
                    </section>
                  );
                })}
              </div>
              {showCanceled && cols.canceled.length > 0 && (
                <section className="section" aria-labelledby="canceled-title" style={{ marginTop: 16 }}>
                  <h2 id="canceled-title">Canceled</h2>
                  <ul className="plain rows">
                    {cols.canceled.map((c) => (
                      <li key={c.name} className="row">
                        <a href={`#/tracker/${spaceId}/issue/${idOf(c.name)}`}>{titleOf(c.name)}</a>
                      </li>
                    ))}
                  </ul>
                </section>
              )}
              {asking && (
                <RequiredDialog
                  move={{ to: asking.plan.to, label: asking.label, required: asking.required }}
                  options={asking.options}
                  onCancel={() => setAsking(undefined)}
                  onConfirm={(fields) => {
                    const a = asking;
                    setAsking(undefined);
                    void carryOut(a.card, a.plan, fields);
                  }}
                />
              )}
            </>
          )
        }
      </Loading>
    </TrackerFrame>
  );
}
