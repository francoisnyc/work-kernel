// The inbox, built from the Types: for every transition whose `allowed`
// names field:X, the Items in its from-state where X is the caller. No
// Type is named here; the vacation request's approvals are what the Types
// declare today.
import { useState, type ReactElement } from "react";
import { useSession } from "../app/session";
import { useLoad } from "../app/useLoad";
import { TransitionOutcome, type Item } from "../gen/kernel/v1/kernel_pb";
import { inboxQueries, inboxTransitions, itemId, parseTypeName } from "../model/typeinfo";
import { ItemMeta } from "../ui/ItemList";
import { RefusalView } from "../ui/RefusalView";
import { TransitionPanel } from "../ui/TransitionPanel";

// The last move made from the inbox: kept on the page, since a row whose
// Item moved on leaves the list.
type Last = { name: string; title: string; toState: string; outcome: string; reason: string };

export function InboxPage(): ReactElement {
  const session = useSession();
  const queries = inboxQueries(session.types);
  const [last, setLast] = useState<Last>();

  async function read(): Promise<Item[]> {
    const found = new Map<string, Item>();
    for (const q of queries) {
      const res = await session.api.searchItems({
        parent: session.parent,
        minSeq: session.minSeq,
        pageSize: 100,
        filter: {
          types: [q.type],
          states: [q.state],
          fields: [{ field: q.field, eq: { kind: { case: "stringValue", value: "me" } } }],
        },
      });
      for (const it of res.items) found.set(it.name, it);
    }
    return [...found.values()];
  }
  const { data: items, error, reload } = useLoad(read, []);
  // A reload that the caller can wait for, after its own write.
  const [waiting, setWaiting] = useState<Item[]>();
  const rows = waiting ?? items ?? [];

  return (
    <>
      <h1>Inbox</h1>
      <p className="hint">
        Items waiting for you:{" "}
        {queries.length === 0
          ? "no Type names a field in a transition's `allowed`."
          : queries.map((q) => `${parseTypeName(q.type).typeId} in ${q.state} where ${q.field} is you`).join("; ")}
      </p>
      <p>
        <button
          type="button"
          className="quiet"
          onClick={() => {
            setWaiting(undefined);
            reload();
          }}
        >
          Refresh
        </button>
      </p>
      {error !== undefined && <RefusalView error={error} />}
      {last && !rows.some((r) => r.name === last.name) && (
        <p role="status" className={`outcome outcome-${last.outcome.toLowerCase()}`}>
          {last.title}: move to {last.toState}: <strong>{last.outcome}</strong> {last.reason}
        </p>
      )}
      {rows.length === 0 && <p className="hint">Nothing is waiting for you.</p>}
      <ul aria-label="Inbox" className="items">
        {rows.map((it) => {
          const type = session.typeOf(it.type);
          if (!type) return null;
          return (
            <li key={it.name}>
              <a href={`#/items/${itemId(it.name)}`}>{session.titleOf(it)}</a> <ItemMeta item={it} />
              <TransitionPanel
                type={type}
                item={it}
                principals={session.principals}
                only={inboxTransitions(type, it)}
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
                  // Refreshed before the answer is shown: an Item that
                  // moved on has left the list by then.
                  setWaiting(await read());
                  return res;
                }}
              />
            </li>
          );
        })}
      </ul>
    </>
  );
}
