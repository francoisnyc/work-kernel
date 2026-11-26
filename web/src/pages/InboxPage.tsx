// The Inbox (track 1 item 5, step 4, PR 2): what waits on the person
// acting, and on each team they are in, as the kernel says it.
//
// The page asks; it decides nothing about who may act:
//   - WhoAmI, once: the caller's roles, for the tabs;
//   - CountItems per tab: the number on the tab ("50+" when capped);
//   - SearchItems with waiting_on: the rows, oldest waiting first;
//   - each row's buttons are the Item's `moves`, as the server lists them,
//     and each button is one TransitionItem. Claiming and releasing are
//     moves like any other.
import { useEffect, useRef, useState, type FormEvent, type ReactElement } from "react";
import { useSession } from "../app/session";
import { TransitionOutcome, type Item } from "../gen/kernel/v1/kernel_pb";
import { isCustomer } from "../model/customers";
import {
  askedBy,
  countLabel,
  inboxCount,
  inboxSearch,
  inboxTabs,
  rowMoves,
  waitingFor,
  whyYours,
  type InboxOrder,
  type InboxTab,
} from "../model/inbox";
import { itemId } from "../model/typeinfo";
import { ItemMeta } from "../ui/ItemList";
import { RefusalView } from "../ui/RefusalView";
import { TransitionPanel } from "../ui/TransitionPanel";

// The last move made from the inbox: kept on the page, since a row whose
// Item moved on leaves the list.
type Last = { name: string; title: string; toState: string; outcome: string; reason: string };
type Count = { count: number; capped: boolean };

export function InboxPage(): ReactElement {
  const session = useSession();
  // Nothing waits on a customer yet (a request sent back to its requester
  // is step 5): no call is made for them.
  if (isCustomer(session.user)) {
    return (
      <>
        <h1>Inbox</h1>
        <p className="hint">Nothing waits on you here yet.</p>
      </>
    );
  }
  return <StaffInbox />;
}

function StaffInbox(): ReactElement {
  const session = useSession();
  const [roles, setRoles] = useState<string[]>();
  const [tabKey, setTabKey] = useState("me");
  const [order, setOrder] = useState<InboxOrder>("oldest");
  const [place, setPlace] = useState(""); // the applied place: an Item's name, or ""
  const [placeText, setPlaceText] = useState("");
  const [rows, setRows] = useState<Item[]>();
  const [next, setNext] = useState("");
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
      const res = await session.api.searchItems(inboxSearch(view, { minSeq: session.minSeq }));
      if (mine !== reading.current) return;
      setRows(res.items);
      setNext(res.nextPageToken);
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

  async function more() {
    const mine = ++reading.current;
    setLoading(true);
    try {
      const res = await session.api.searchItems(inboxSearch(view, { pageToken: next, minSeq: session.minSeq }));
      if (mine !== reading.current) return;
      setRows((old) => [...(old ?? []), ...res.items]);
      setNext(res.nextPageToken);
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

  const now = new Date();
  const shown = rows ?? [];
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
      {last && !shown.some((r) => r.name === last.name) && (
        <p role="status" className={`outcome outcome-${last.outcome.toLowerCase()}`}>
          {last.title}: move to {last.toState}: <strong>{last.outcome}</strong> {last.reason}
        </p>
      )}
      {rows !== undefined && rows.length === 0 && error === undefined && (
        <p className="hint">{tab.role === undefined ? "Nothing is waiting on you." : `Nothing is waiting on ${tab.role}.`}</p>
      )}
      <ul aria-label="Inbox" className={loading ? "items loading" : "items"} aria-busy={loading}>
        {shown.map((it) => {
          const type = session.typeOf(it.type);
          const moves = rowMoves(type, it);
          const about = [`asked by ${askedBy(it)}`, waitingFor(it.stateTime, now), whyYours(tab, type, it, session.user.principal)];
          return (
            <li key={it.name}>
              <a href={`#/items/${itemId(it.name)}`}>{session.titleOf(it)}</a> <ItemMeta item={it} />
              <p className="meta">{about.filter((s) => s !== "").join(" · ")}</p>
              {type && moves.length > 0 ? (
                <TransitionPanel
                  type={type}
                  item={it}
                  principals={session.principals}
                  only={moves}
                  onTransition={async (toState, fields, requestId) => {
                    const res = await session.api.transitionItem({
                      name: it.name,
                      requestId,
                      toState,
                      fields,
                      wait: { seconds: 5n },
                    });
                    session.wrote(res.commitSeq);
                    setLast({
                      name: it.name,
                      title: session.titleOf(it),
                      toState,
                      outcome: TransitionOutcome[res.outcome],
                      reason: res.reason,
                    });
                    // Read again before the answer is shown: an Item that
                    // moved on has left the list by then, and the tabs'
                    // numbers follow.
                    await Promise.all([readRows(), readCounts()]);
                    return res;
                  }}
                />
              ) : (
                <p className="hint">No move here is yours to make.</p>
              )}
            </li>
          );
        })}
      </ul>
      {next !== "" && (
        <p>
          <button type="button" disabled={loading} onClick={() => void more()}>
            More
          </button>
        </p>
      )}
    </>
  );
}
