// The Inbox (track 1 item 5, step 4, PR 2; the Inbox for people, PR 2b):
// what waits on the person acting, and on each team they are in, as the
// kernel says it, in words.
//
// The page asks; it decides nothing about who may act:
//   - WhoAmI, once: the caller's roles, for the tabs;
//   - CountItems per tab: the number on the tab ("50+" when capped), a
//     team's queue only;
//   - SearchItems with waiting_on: the rows, oldest waiting first; on a
//     team's tab also its claimed rows (filter.claimed), "Being handled";
//   - each row's buttons are the Item's `moves`, as the server lists them
//     (the primary one, else the first, as the main button; the rest
//     behind More moves), each one TransitionItem; why a move is withheld
//     is the Item's `withheld`, in words.
import { useEffect, useId, useRef, useState, type FormEvent, type ReactElement } from "react";
import { useSession } from "../app/session";
import { TransitionOutcome, type Item, type TransitionItemResponse } from "../gen/kernel/v1/kernel_pb";
import type { JsonObject } from "@bufbuild/protobuf";
import {
  askedLine,
  countLabel,
  dueOf,
  groupRows,
  holderLine,
  inboxCount,
  inboxSearch,
  inboxTabs,
  splitMoves,
  stateLabel,
  summaryLine,
  waitedLine,
  whyNot,
  whyYours,
  type InboxOrder,
  type InboxTab,
} from "../model/inbox";
import { itemId } from "../model/typeinfo";
import { RefusalView } from "../ui/RefusalView";
import { TransitionPanel } from "../ui/TransitionPanel";

// The last move made from the inbox, said by the page: a row whose Item
// moved on leaves its list, or moves to another (a claim).
type Last = { name: string; title: string; move: string; outcome: string; reason: string };
type Count = { count: number; capped: boolean };
type Move = (it: Item, toState: string, fields: JsonObject, requestId: string) => Promise<TransitionItemResponse>;

// A customer's Inbox is the same page: they hold no role, so "Waiting on
// me" is their one tab, and it holds what was sent back to them (step 5).
export function InboxPage(): ReactElement {
  const session = useSession();
  const [roles, setRoles] = useState<string[]>();
  const [tabKey, setTabKey] = useState("me");
  const [order, setOrder] = useState<InboxOrder>("oldest");
  const [place, setPlace] = useState(""); // the applied place: an Item's name, or ""
  const [placeText, setPlaceText] = useState("");
  const [rows, setRows] = useState<Item[]>();
  const [next, setNext] = useState("");
  // A team tab's claimed rows ("Being handled") and their next page.
  const [held, setHeld] = useState<Item[]>([]);
  const [nextHeld, setNextHeld] = useState("");
  const [counts, setCounts] = useState<Record<string, Count>>({});
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>();
  const [last, setLast] = useState<Last>();
  const [tick, setTick] = useState(0);
  // The newest read wins: an older, slower answer is dropped.
  const reading = useRef(0);

  const tabs = inboxTabs(roles ?? [], session.types);
  const tab: InboxTab = tabs.find((t) => t.key === tabKey) ?? tabs[0]!;
  const view = { parent: session.parent, tab, order, place };
  const team = tab.role !== undefined;

  useEffect(() => {
    session.api.whoAmI({}).then(
      (me) => setRoles(me.roles),
      (e) => setError(e),
    );
  }, [session]);

  async function readCounts(): Promise<void> {
    const got: Record<string, Count> = {};
    await Promise.all(
      tabs.map(async (t) => {
        const c = await session.api.countItems(inboxCount({ parent: session.parent, tab: t, place }));
        got[t.key] = { count: c.count, capped: c.capped };
      }),
    );
    setCounts(got);
  }

  async function readRows(): Promise<void> {
    const mine = ++reading.current;
    setLoading(true);
    try {
      const [res, claimed] = await Promise.all([
        session.api.searchItems(inboxSearch(view, { minSeq: session.minSeq })),
        team ? session.api.searchItems(inboxSearch({ ...view, claimed: true }, { minSeq: session.minSeq })) : undefined,
      ]);
      if (mine !== reading.current) return;
      setRows(res.items);
      setNext(res.nextPageToken);
      setHeld(claimed?.items ?? []);
      setNextHeld(claimed?.nextPageToken ?? "");
      setError(undefined);
    } catch (e) {
      if (mine === reading.current) setError(e);
    } finally {
      if (mine === reading.current) setLoading(false);
    }
  }

  // The rows: read again when the tab, the order or the place changes.
  useEffect(() => {
    if (roles === undefined) return;
    void readRows();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [roles, tab.key, order, place, tick]);

  // The counts: one per tab, read again when the place changes.
  useEffect(() => {
    if (roles === undefined) return;
    readCounts().catch(setError);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [roles, place, tick]);

  // The next page of one section: the queue (or Waiting on me), or a team
  // tab's claimed rows.
  async function more(claimed: boolean) {
    const mine = ++reading.current;
    setLoading(true);
    try {
      const res = await session.api.searchItems(
        inboxSearch(claimed ? { ...view, claimed: true } : view, { pageToken: claimed ? nextHeld : next, minSeq: session.minSeq }),
      );
      if (mine !== reading.current) return;
      if (claimed) {
        setHeld((old) => [...old, ...res.items]);
        setNextHeld(res.nextPageToken);
      } else {
        setRows((old) => [...(old ?? []), ...res.items]);
        setNext(res.nextPageToken);
      }
      setError(undefined);
    } catch (e) {
      if (mine === reading.current) setError(e);
    } finally {
      if (mine === reading.current) setLoading(false);
    }
  }

  function applyPlace(e: FormEvent) {
    e.preventDefault();
    setPlace(session.itemName(placeText));
  }

  const move: Move = async (it, toState, fields, requestId) => {
    const res = await session.api.transitionItem({ name: it.name, requestId, toState, fields, wait: { seconds: 5n } });
    session.wrote(res.commitSeq);
    const tr = session.typeOf(it.type)?.transitions.find((t) => t.fromState === it.state && t.toState === toState);
    setLast({
      name: it.name,
      title: session.titleOf(it),
      move: tr?.label || `Move to ${toState}`,
      outcome: TransitionOutcome[res.outcome],
      reason: res.reason,
    });
    // Read again before the answer is shown: an Item that moved on has
    // left the list by then, and the tabs' numbers follow.
    await Promise.all([readRows(), readCounts()]);
    return res;
  };

  const now = new Date();
  const shown = rows ?? [];
  const everything = [...shown, ...held];
  const groups = groupRows(shown, session.typeOf.bind(session), now);
  const section = (label: string, items: Item[]) =>
    items.length > 0 && (
      <>
        <h2>{label}</h2>
        <ul aria-label={label} className="items">
          {items.map((it) => (
            <Row key={it.name} item={it} tab={tab} now={now} onMove={move} />
          ))}
        </ul>
      </>
    );
  return (
    <>
      <h1>Inbox</h1>
      <div role="tablist" aria-label="Queues" className="tabs">
        {tabs.map((t) => (
          <button key={t.key} type="button" role="tab" aria-selected={t.key === tab.key} onClick={() => setTabKey(t.key)}>
            {t.label} <span className="count">{countLabel(counts[t.key])}</span>
          </button>
        ))}
      </div>
      <form className="inbox-controls" onSubmit={applyPlace}>
        <label htmlFor="inbox-order">Order</label>
        <select id="inbox-order" value={order} onChange={(e) => setOrder(e.target.value as InboxOrder)}>
          <option value="oldest">Oldest waiting first</option>
          <option value="newest">Newest waiting first</option>
        </select>
        <label htmlFor="inbox-place">Place</label>
        <input
          id="inbox-place"
          value={placeText}
          placeholder="an Item's name or ID; empty for anywhere"
          onChange={(e) => setPlaceText(e.target.value)}
        />
        <button type="submit" className="quiet">
          Apply
        </button>
        <button type="button" className="quiet" onClick={() => setTick((t) => t + 1)}>
          Refresh
        </button>
      </form>
      {error !== undefined && <RefusalView error={error} />}
      {last && (
        <p role="status" className={`outcome outcome-${last.outcome.toLowerCase()}`}>
          {last.title}: {last.move}: <strong>{last.outcome}</strong> {last.reason}
        </p>
      )}
      {rows !== undefined && everything.length === 0 && error === undefined && (
        <p className="hint">{tab.role === undefined ? "Nothing is waiting on you." : `Nothing is waiting on ${tab.label}.`}</p>
      )}
      <section aria-label="Inbox" aria-busy={loading} className={loading ? "inbox loading" : "inbox"}>
        {team ? (
          <>
            {section("Nobody has these yet", shown)}
            {next !== "" && (
              <p>
                <button type="button" disabled={loading} onClick={() => void more(false)}>
                  More
                </button>
              </p>
            )}
            {section("Being handled", held)}
            {nextHeld !== "" && (
              <p>
                <button type="button" disabled={loading} onClick={() => void more(true)}>
                  More being handled
                </button>
              </p>
            )}
          </>
        ) : (
          <>
            {section("Overdue", groups.overdue)}
            {section("Waiting", groups.waiting)}
            {next !== "" && (
              <p>
                <button type="button" disabled={loading} onClick={() => void more(false)}>
                  More
                </button>
              </p>
            )}
          </>
        )}
      </section>
    </>
  );
}

// One row, in words: its title; the Type's summary fields; its Type and
// state; who asked, why it is the caller's, how long it waited and when
// it is due; who holds its claim and why a move is withheld; its main
// move, and the rest behind More moves.
function Row({ item: it, tab, now, onMove }: { item: Item; tab: InboxTab; now: Date; onMove: Move }): ReactElement {
  const session = useSession();
  const [open, setOpen] = useState(false);
  const moresId = useId();
  const type = session.typeOf(it.type);
  const me = session.user.principal;
  const { main, others } = splitMoves(type, it);
  const summary = summaryLine(type, it, session.nameOf);
  const due = dueOf(type, it, now);
  const about = [askedLine(it, me, session.nameOf), whyYours(tab, type, it, me), waitedLine(type, it, now)].filter((s) => s !== "");
  const why = [holderLine(type, it, me, session.nameOf), ...whyNot(type, it, me, session.nameOf)].filter(
    (s, i, all) => s !== "" && all.indexOf(s) === i,
  );
  const typeId = type?.typeId ?? it.type.split("/types/")[1]?.split("/")[0] ?? "";
  const make = (toState: string, fields: JsonObject, requestId: string) => onMove(it, toState, fields, requestId);
  return (
    <li>
      <a href={`#/items/${itemId(it.name)}`}>{session.titleOf(it)}</a>
      {summary.length > 0 && (
        <p className="summary">
          {summary.map((s, i) => (
            <span key={s}>
              {i > 0 && " · "}
              <span>{s}</span>
            </span>
          ))}
        </p>
      )}
      <p className="kind">
        {typeId} · {stateLabel(type, it.state)}
      </p>
      <p className="meta">
        {about.join(" · ")}
        {due && (
          <>
            {" · "}
            <span className={due.overdue ? "due overdue" : "due"}>{due.text}</span>
          </>
        )}
      </p>
      {why.length > 0 && (
        <p className="why-not">
          {why.map((s, i) => (
            <span key={s}>
              {i > 0 && " · "}
              <span>{s}</span>
            </span>
          ))}
        </p>
      )}
      {type && main ? (
        <div className="row-moves">
          <TransitionPanel type={type} item={it} principals={session.principals} only={[main]} compact onTransition={make} />
          {others.length > 0 && (
            <>
              <button type="button" className="quiet" aria-expanded={open} aria-controls={moresId} onClick={() => setOpen((o) => !o)}>
                More moves
              </button>
              <div id={moresId} hidden={!open}>
                <TransitionPanel type={type} item={it} principals={session.principals} only={others} compact onTransition={make} />
              </div>
            </>
          )}
        </div>
      ) : (
        // No move of theirs here (a customer answers by replying): the
        // Item's own page is where to act.
        <p>
          <a href={`#/items/${itemId(it.name)}`}>Open it to answer</a>
        </p>
      )}
    </li>
  );
}
