// A board: the Items linked to one Item by one link (a sprint's
// sprint_issues), ordered by a rank field, re-ordered by dragging. It is a
// search with a link condition, never a container listing; a drop is a
// rank move against the moved card's container (model/rankmove.ts).
import { useRef, useState, type DragEvent, type ReactElement } from "react";
import { go } from "../app/router";
import { useSession } from "../app/session";
import { useLoad } from "../app/useLoad";
import { FieldType, LinkDirection, type Item } from "../gen/kernel/v1/kernel_pb";
import { dropToMove, type BoardCard } from "../model/board";
import { rankMove } from "../model/rankmove";
import { emptySearch, toSearchRequest } from "../model/search";
import { incomingLinkTypes, itemId, latestVersions } from "../model/typeinfo";
import { RankError } from "../rank/rank";
import { ItemMeta } from "../ui/ItemList";
import { RefusalView } from "../ui/RefusalView";

const PAGE = 100;

const RANK_REFUSALS: Record<string, string> = {
  changed: "The cards there kept moving while the move was planned; nothing was written. Try again.",
  no_rank: "The other card has no rank yet: rank it first.",
  tied_run_too_long:
    "Too many cards there share one rank key to re-key in a move; nothing was written. This container needs a rebalance (pli rank rebalance).",
};

export function BoardPage({ id, link, rankField }: { id: string; link: string; rankField: string }): ReactElement {
  const session = useSession();
  const name = `${session.parent}/items/${id}`;
  const [cards, setCards] = useState<Item[]>([]);
  const [next, setNext] = useState("");
  const [notice, setNotice] = useState("");
  const [rebalance, setRebalance] = useState(false);
  const [failure, setFailure] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  const dragged = useRef<number>(-1);

  const { data: item, error } = useLoad(async () => (await session.api.getItem({ name })).item, [name]);
  const type = item ? session.typeOf(item.type) : undefined;
  // How the linked Items see the link: a link other Types declare towards
  // this Item is OUTGOING for them, under its declared name; a link this
  // Item's Type declares is INCOMING for them, under its inverse.
  const incoming = type ? incomingLinkTypes(session.types, type.typeId).find((l) => l.name === link) : undefined;
  const own = type?.linkTypes.find((lt) => lt.name === link);
  const linked = incoming
    ? { direction: LinkDirection.OUTGOING, linkType: incoming.declared, item: name }
    : own
      ? { direction: LinkDirection.INCOMING, linkType: own.inverse, item: name }
      : undefined;
  // The string fields of the Types whose Items can be on this board.
  const cardTypes = latestVersions(session.types).filter((t) => !incoming || incoming.from.includes(t.typeId));
  const rankFields = [
    ...new Set(cardTypes.flatMap((t) => t.fields.filter((f) => f.type === FieldType.STRING).map((f) => f.id))),
  ].sort();

  async function load(pageToken: string) {
    if (!linked) return;
    const form = { ...emptySearch, orderBy: rankField, linked };
    const res = await session.api.searchItems(
      toSearchRequest(session.ns, form, { minSeq: session.minSeq, pageToken, pageSize: PAGE }),
    );
    setCards((old) => (pageToken === "" ? res.items : [...old, ...res.items]));
    setNext(res.nextPageToken);
  }
  const { error: loadError, reload } = useLoad(() => load(""), [linked?.linkType, linked?.direction, rankField]);

  const keyOf = (it: Item) => (typeof it.fields?.[rankField] === "string" ? String(it.fields[rankField]) : "");
  const asCards: BoardCard[] = cards.map((it) => ({ name: it.name, container: it.container, key: keyOf(it) }));

  // Puts the card at index `from` at index `to` (counted without it).
  async function move(from: number, to: number) {
    const moved = cards[from];
    const drop = dropToMove(asCards, from, to);
    setNotice("");
    setFailure(undefined);
    if (!moved || drop.kind === "none") return;
    if (drop.kind === "refused") {
      setNotice("A rank orders one container: the cards next to that place are in another container, so no move was made.");
      return;
    }
    setBusy(true);
    try {
      const res = await rankMove(session.api, session.ns, moved, drop.pos, drop.other?.name, rankField);
      session.wrote(res.commitSeq);
      if (res.needsRebalance) setRebalance(true);
    } catch (e) {
      if (e instanceof RankError) setNotice(RANK_REFUSALS[e.code] ?? e.message);
      else setFailure(e);
    } finally {
      setBusy(false);
      // The order shown is the server's: read it again.
      await load("").catch(setFailure);
    }
  }

  function onDrop(e: DragEvent<HTMLLIElement>, target: number) {
    e.preventDefault();
    const from = dragged.current;
    dragged.current = -1;
    if (from < 0) return;
    // The top half of a card: before it. The bottom half: after it.
    const box = e.currentTarget.getBoundingClientRect();
    const before = e.clientY < box.top + box.height / 2;
    const without = target > from ? target - 1 : target;
    void move(from, before ? without : without + 1);
  }

  if (error !== undefined) return <RefusalView error={error} />;
  if (!item || !type) return <p>Loading…</p>;
  if (!linked) return <p>No link named {link} reaches a {type.typeId}.</p>;
  return (
    <>
      <h1>
        Board: {link} of <a href={`#/items/${id}`}>{session.titleOf(item)}</a>
      </h1>
      <p className="meta">
        Ordered by{" "}
        <select
          aria-label="Rank field"
          value={rankField}
          onChange={(e) => go(`#/board/${id}/${link}?rank=${encodeURIComponent(e.target.value)}`)}
        >
          {[...new Set([rankField, ...rankFields])].map((f) => (
            <option key={f} value={f}>
              {f}
            </option>
          ))}
        </select>{" "}
        <button type="button" className="quiet" onClick={reload}>
          Refresh
        </button>{" "}
        Drag a card onto the top or bottom half of another, or use its arrows.
      </p>
      {rebalance && (
        <p role="note" className="warning">
          This container needs a rebalance: its rank keys have grown long. Moves still work; run pli rank rebalance when
          nobody is dragging cards.
        </p>
      )}
      {notice !== "" && <p role="alert">{notice}</p>}
      {failure !== undefined && <RefusalView error={failure} />}
      {loadError !== undefined && <RefusalView error={loadError} />}
      {cards.length === 0 && <p className="hint">No Items are linked here.</p>}
      <ul aria-label="Board" className={busy ? "board busy" : "board"}>
        {cards.map((it, i) => {
          const title = session.titleOf(it);
          return (
            <li
              key={it.name}
              draggable
              onDragStart={(e) => {
                dragged.current = i;
                e.dataTransfer.effectAllowed = "move";
                e.dataTransfer.setData("text/plain", it.name);
              }}
              onDragOver={(e) => e.preventDefault()}
              onDrop={(e) => onDrop(e, i)}
            >
              <a href={`#/items/${itemId(it.name)}`}>{title}</a> <ItemMeta item={it} />
              {keyOf(it) === "" && <span className="hint"> not ranked</span>}
              <span className="arrows">
                <button type="button" aria-label={`Move ${title} up`} disabled={busy || i === 0} onClick={() => move(i, i - 1)}>
                  ↑
                </button>
                <button
                  type="button"
                  aria-label={`Move ${title} down`}
                  disabled={busy || i === cards.length - 1}
                  onClick={() => move(i, i + 1)}
                >
                  ↓
                </button>
              </span>
            </li>
          );
        })}
      </ul>
      {next !== "" && (
        <button type="button" onClick={() => load(next)}>
          Load more
        </button>
      )}
    </>
  );
}
