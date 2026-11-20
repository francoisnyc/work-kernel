// One Item, whatever its Type: fields (a form built from GetType), state
// and the moves its Type declares, links both ways, rollups, the Items it
// contains, and its history. Every section reloads after an action.
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useState, type ReactElement } from "react";
import { useSession } from "../app/session";
import { useLoad } from "../app/useLoad";
import { ItemEventKind, LinkDirection, type Item, type ItemEvent, type ItemRef, type Type } from "../gen/kernel/v1/kernel_pb";
import { linkWrite } from "../model/links";
import { emptySearch } from "../model/search";
import { incomingLinkTypes, itemId, shortId } from "../model/typeinfo";
import { onlySetOps } from "../model/values";
import { ItemForm } from "../ui/ItemForm";
import { ItemList, categoryName } from "../ui/ItemList";
import { LinksPanel } from "../ui/LinksPanel";
import { RefusalView } from "../ui/RefusalView";
import { TransitionPanel } from "../ui/TransitionPanel";
import { searchHash } from "./ItemsPage";

export function ItemPage({ id }: { id: string }): ReactElement {
  const session = useSession();
  const name = `${session.parent}/items/${id}`;
  // One counter reloads every section: bumped after each action.
  const [version, setVersion] = useState(0);
  const refresh = () => setVersion((v) => v + 1);
  const { data: item, error } = useLoad(async () => (await session.api.getItem({ name })).item, [name, version]);

  // The titles of the Items this one links to and of its container: one
  // read each, for the first 40 (a link itself carries only a name).
  const others = [...new Set([item?.container ?? "", ...(item?.links ?? []).map((l) => l.item?.name ?? "")])]
    .filter((n) => n !== "")
    .slice(0, 40);
  const { data: titles } = useLoad(async () => {
    const found: Record<string, string> = {};
    await Promise.all(
      others.map(async (n) => {
        const other = (await session.api.getItem({ name: n }).catch(() => undefined))?.item;
        if (other) found[n] = session.titleOf(other);
      }),
    );
    return found;
  }, [others.join(" ")]);

  if (error !== undefined) return <RefusalView error={error} />;
  if (!item) return <p>Loading…</p>;
  const type = session.typeOf(item.type);
  if (!type) return <p>This Item's Type ({item.type}) is not among the Types the server lists.</p>;

  // The other Item's Type declares an incoming link: read it there.
  async function sourceType(direction: LinkDirection, other: string, own: Type): Promise<Type> {
    if (direction !== LinkDirection.INCOMING) return own;
    const source = (await session.api.getItem({ name: other })).item!;
    return session.typeOf(source.type) ?? own;
  }

  const boards = [
    ...incomingLinkTypes(session.types, type.typeId).map((l) => l.name),
    ...type.linkTypes.filter((lt) => !lt.maxOne).map((lt) => lt.name),
  ];

  return (
    <>
      <h1>{session.titleOf(item)}</h1>
      <p className="meta">
        {type.typeId}@{type.version} · state <strong data-testid="item-state">{item.state}</strong> · category{" "}
        {categoryName(item.category)} ·{" "}
        {item.container !== "" ? (
          <>
            in <a href={`#/items/${itemId(item.container)}`}>{titles?.[item.container] ?? shortId(item.container)}</a>
          </>
        ) : (
          "at the top level"
        )}{" "}
        · by {item.creator}
        {item.creatorOnBehalfOf !== "" && ` for ${item.creatorOnBehalfOf}`}
        {item.createTime && ` · ${timestampDate(item.createTime).toLocaleString()}`} · <code>{id}</code>{" "}
        <button type="button" className="quiet" onClick={refresh}>
          Refresh
        </button>
      </p>

      <section aria-labelledby="h-moves">
        <h2 id="h-moves">Transitions</h2>
        <TransitionPanel
          type={type}
          item={item}
          principals={session.principals}
          onTransition={async (toState, fields, requestId) => {
            try {
              // Always with a wait: the server ignores it on a move that is
              // not held, so the UI need not know which are.
              const res = await session.api.transitionItem({ name, requestId, toState, fields, wait: { seconds: 5n } });
              session.wrote(res.commitSeq);
              return res;
            } finally {
              refresh();
            }
          }}
        />
      </section>

      <section aria-labelledby="h-fields">
        <h2 id="h-fields">Fields</h2>
        <ItemForm
          // A new form when the Item changed: its values and what is frozen.
          key={`${item.etag}/${item.state}`}
          type={type}
          item={item}
          principals={session.principals}
          submitLabel="Save"
          onSubmit={async (fields) => {
            // With the etag: the user edited what they were shown. Except
            // a save that only adds to or removes from sets: those merge
            // with anyone else's change, so they need no etag.
            const etag = onlySetOps(type, fields) ? "" : item.etag;
            const res = await session.api.updateItem({ name, requestId: crypto.randomUUID(), etag, fields });
            session.wrote(res.commitSeq);
            refresh();
          }}
        />
      </section>

      <section aria-labelledby="h-links">
        <h2 id="h-links">Links</h2>
        <LinksPanel
          item={item}
          type={type}
          types={session.types}
          search={session.search}
          titleOf={session.titleOf}
          titles={titles}
          onLink={async (direction, linkType, other) => {
            const otherName = session.itemName(other);
            const w = linkWrite(item, direction, linkType, otherName, await sourceType(direction, otherName, type));
            const res = await session.api.linkItems({ ...w, requestId: crypto.randomUUID() });
            session.wrote(res.commitSeq);
            refresh();
          }}
          onUnlink={async (link) => {
            const other = link.item?.name ?? "";
            const w = linkWrite(item, link.direction, link.linkType, other, await sourceType(link.direction, other, type));
            const res = await session.api.unlinkItems({ ...w, requestId: crypto.randomUUID() });
            session.wrote(res.commitSeq);
            refresh();
          }}
        />
        {item.linksTruncated && (
          <p>
            See all:{" "}
            {[...new Set(item.links.map((l) => `${l.direction}|${l.linkType}`))].map((key) => {
              const [direction, linkType] = key.split("|") as [string, string];
              // The linked Items see the link from the other end.
              const theirs =
                Number(direction) === LinkDirection.INCOMING
                  ? { direction: LinkDirection.OUTGOING, linkType: incomingLinkTypes(session.types, type.typeId).find((l) => l.name === linkType)?.declared ?? linkType }
                  : { direction: LinkDirection.INCOMING, linkType: type.linkTypes.find((lt) => lt.name === linkType)?.inverse ?? linkType };
              return (
                <a key={key} href={searchHash({ ...emptySearch, linked: { ...theirs, item: name } })}>
                  {linkType}{" "}
                </a>
              );
            })}
          </p>
        )}
        {boards.length > 0 && (
          <p className="boards">
            {boards.map((b) => (
              <a key={b} href={`#/board/${id}/${b}`}>
                Board: {b}
              </a>
            ))}
          </p>
        )}
      </section>

      {type.rollups.length > 0 && (
        <section aria-labelledby="h-rollups">
          <h2 id="h-rollups">Rollups</h2>
          <p className="hint">Counted when read, over the linked Items you can see.</p>
          <dl>
            {type.rollups.map((r) => {
              const counts = Object.entries(item.rollups[r.id]?.counts ?? {});
              return (
                <div key={r.id}>
                  <dt>
                    {r.id} <span className="meta">({r.over} by {r.countBy})</span>
                  </dt>
                  <dd>{counts.length === 0 ? "nothing to count" : counts.map(([k, n]) => `${k}: ${n}`).join(", ")}</dd>
                </div>
              );
            })}
          </dl>
        </section>
      )}

      <Children key={`c${version}`} item={item} />
      <History key={`h${version}`} name={name} />
    </>
  );
}

// The Items contained in this one (a space's issues, an issue's comments).
function Children({ item }: { item: Item }): ReactElement {
  const session = useSession();
  const [items, setItems] = useState<Item[]>([]);
  const [next, setNext] = useState("");
  async function more(pageToken: string) {
    const res = await session.api.listItems({ parent: item.name, pageSize: 50, pageToken });
    setItems((old) => (pageToken === "" ? res.items : [...old, ...res.items]));
    setNext(res.nextPageToken);
  }
  const { error } = useLoad(() => more(""), [item.name]);
  return (
    <section aria-labelledby="h-children">
      <h2 id="h-children">Contained Items</h2>
      {error !== undefined && <RefusalView error={error} />}
      <ItemList items={items} label="Contained Items" />
      {next !== "" && (
        <button type="button" onClick={() => more(next)}>
          Load more
        </button>
      )}
      <p>
        <a href={`#/new?in=${itemId(item.name)}`}>Create an Item inside this one</a>
      </p>
    </section>
  );
}

const ref = (r: ItemRef | undefined): ReactElement | string => {
  if (!r) return "the top level";
  if (r.restricted) return "an Item you cannot see";
  return <a href={`#/items/${itemId(r.name)}`}>{shortId(r.name)}</a>;
};

// What one event says, beyond its kind and who did it.
function details(e: ItemEvent): (ReactElement | string)[] {
  const out: (ReactElement | string)[] = [];
  if (e.kind === ItemEventKind.CREATED) out.push(`in state ${e.toState}`);
  if (e.kind === ItemEventKind.TRANSITIONED) out.push(`${e.fromState} → ${e.toState}`);
  if (e.requestedState !== "") out.push(`${e.fromState} → ${e.requestedState} asked`);
  if (e.reason !== "") out.push(`reason ${e.reason}`);
  if (e.hook !== "") out.push(`hook ${e.hook}`);
  if (e.originatingHook !== "") out.push(`from hook ${e.originatingHook}`);
  if (e.reaction !== "") out.push(`reaction ${e.reaction}`);
  if (e.timer !== "") out.push(`timer ${e.timer}, run ${e.run}`);
  if (e.linkType !== "") out.push(<>link {e.linkType} to {ref(e.linkTarget)}</>);
  if (e.linkReplaced) out.push(<>replacing {ref(e.linkReplaced)}</>);
  if (e.container) out.push(<>inside {ref(e.container)}</>);
  if (e.moved) out.push(<>moved from {ref(e.containerFrom)} to {ref(e.containerTo)}</>);
  if (e.readers) out.push(`readers ${e.readers.principals.join(", ")}`);
  if (e.inherit) out.push("readers inherited again");
  if (e.fields && Object.keys(e.fields).length > 0) out.push(`set ${JSON.stringify(e.fields)}`);
  if (e.added && Object.keys(e.added).length > 0) out.push(`added ${JSON.stringify(e.added)}`);
  // What the writer asked of a multi-value field: members added and removed.
  if (e.setOps && Object.keys(e.setOps).length > 0) out.push(`changed ${JSON.stringify(e.setOps)}`);
  if (e.erasedFields.length > 0) out.push(`erased ${e.erasedFields.join(", ")}`);
  return out;
}

// The Item's events, oldest first, as the caller may see them.
function History({ name }: { name: string }): ReactElement {
  const session = useSession();
  const [events, setEvents] = useState<ItemEvent[]>([]);
  const [next, setNext] = useState("");
  async function more(pageToken: string) {
    const res = await session.api.listItemEvents({ parent: name, pageSize: 100, pageToken });
    setEvents((old) => (pageToken === "" ? res.events : [...old, ...res.events]));
    setNext(res.nextPageToken);
  }
  const { error } = useLoad(() => more(""), [name]);
  return (
    <section aria-labelledby="h-history">
      <h2 id="h-history">History</h2>
      {error !== undefined && <RefusalView error={error} />}
      <ol aria-label="History" className="timeline">
        {events.map((e) => (
          <li key={e.name}>
            <span className="meta">
              #{String(e.itemSeq)} {e.createTime ? timestampDate(e.createTime).toLocaleString() : ""}
            </span>{" "}
            <strong>{ItemEventKind[e.kind].toLowerCase()}</strong> by {e.actor}
            {e.onBehalfOf !== "" && ` for ${e.onBehalfOf}`}
            {details(e).map((d, i) => (
              <span key={i} className="rule">
                {d}
              </span>
            ))}
          </li>
        ))}
      </ol>
      {next !== "" && (
        <button type="button" onClick={() => more(next)}>
          Load more
        </button>
      )}
    </section>
  );
}
