// An Item's links, both ways, with add and remove where the link type is
// not the kernel's to set.
import { useId, useState, type ReactElement } from "react";
import { FieldWriter, LinkDirection, type Item, type Link, type Type } from "../gen/kernel/v1/kernel_pb";
import { linkRows } from "../model/links";
import { incomingLinkTypes, itemId, shortId } from "../model/typeinfo";
import { ItemPicker, type ItemSearch } from "./ItemPicker";
import { RefusalView } from "./RefusalView";

export type LinksPanelProps = {
  item: Item;
  type: Type;
  // Every Type (ListTypes): what may link to this Item is declared there.
  types: Type[];
  // linkType as this Item sees it (the inverse name for INCOMING); other:
  // the other Item's name.
  onLink: (direction: LinkDirection, linkType: string, other: string) => Promise<void>;
  onUnlink: (link: Link) => Promise<void>;
  // Finds Items by words, for the picker; without it the picker takes a
  // pasted Item name only.
  search?: ItemSearch;
  titleOf?: (item: Item) => string;
  // The other Items' titles by name, where the page has read them; a link
  // always shows the other Item's short ID.
  titles?: Record<string, string>;
  // The other Items' states by name, where the page has read them (Jira
  // part 2, step 3b): shown beside the link, so a page shows the state of
  // what it is linked to with no rule carrying it. A restricted end has none.
  states?: Record<string, string>;
};

// A link type that can be added from this Item's page.
type Addable = { direction: LinkDirection; name: string; typeNames: string[] };

export function LinksPanel({ item, type, types, onLink, onUnlink, search, titleOf, titles, states }: LinksPanelProps): ReactElement {
  const id = useId();
  const [error, setError] = useState<unknown>();
  const [draft, setDraft] = useState<Record<string, string>>({});
  const ns = item.name.split("/")[1] ?? "";

  const addable: Addable[] = [
    ...type.linkTypes
      .filter((lt) => lt.writableBy !== FieldWriter.KERNEL)
      .map((lt) => ({
        direction: LinkDirection.OUTGOING,
        name: lt.name,
        typeNames: lt.targets.includes("*") ? [] : lt.targets,
      })),
    ...incomingLinkTypes(types, type.typeId)
      .filter((l) => l.writable)
      .map((l) => ({
        direction: LinkDirection.INCOMING,
        name: l.name,
        typeNames: l.from.map((t) => `namespaces/${ns}/types/${t}`),
      })),
  ];

  // Runs a write; a refusal is shown, not thrown.
  async function run(write: () => Promise<void>) {
    setError(undefined);
    try {
      await write();
    } catch (err) {
      setError(err);
    }
  }

  const rows = linkRows(item, type, types);
  return (
    <div className="links">
      {rows.length === 0 && <p className="hint">No links.</p>}
      <ul>
        {rows.map((row, i) => {
          const other = row.link.item?.name ?? "";
          return (
            <li key={i}>
              <span className="link-type">{row.link.linkType}</span>{" "}
              {row.restricted ? (
                <em>restricted (an Item you cannot see)</em>
              ) : (
                <a href={`#/items/${itemId(other)}`}>
                  {titles?.[other] ? `${titles[other]} (${shortId(other)})` : shortId(other)}
                </a>
              )}
              {!row.restricted && states?.[other] && (
                <span className="meta" data-testid="link-state">
                  {" "}
                  · {states[other]}
                </span>
              )}
              {row.kernel && <span className="hint"> set by the kernel</span>}
              {row.removable && (
                <button
                  type="button"
                  className="quiet"
                  aria-label={`Remove ${row.link.linkType} ${shortId(other)}`}
                  onClick={() => run(() => onUnlink(row.link))}
                >
                  Remove
                </button>
              )}
            </li>
          );
        })}
      </ul>
      {item.linksTruncated && <p className="hint">More links exist than are shown here.</p>}
      {addable.map((a) => {
        const inputId = `${id}-${a.direction}-${a.name}`;
        const value = draft[inputId] ?? "";
        return (
          <div key={inputId} className="add-link">
            <label htmlFor={inputId}>Item to add as {a.name}</label>
            <ItemPicker
              id={inputId}
              value={value}
              onChange={(v) => setDraft((old) => ({ ...old, [inputId]: v }))}
              search={search}
              typeNames={a.typeNames}
              titleOf={titleOf}
            />
            <button
              type="button"
              onClick={() => {
                if (value.trim() === "") return;
                void run(async () => {
                  await onLink(a.direction, a.name, value.trim());
                  setDraft((old) => ({ ...old, [inputId]: "" }));
                });
              }}
            >
              Add {a.name}
            </button>
          </div>
        );
      })}
      {error !== undefined && <RefusalView error={error} />}
    </div>
  );
}
