import type { ReactElement } from "react";
import { useSession } from "../app/session";
import { StateCategory, type Item } from "../gen/kernel/v1/kernel_pb";
import { itemId, parseTypeName } from "../model/typeinfo";

export const categoryName = (c: StateCategory) => StateCategory[c].toLowerCase();

// One line about an Item: what every list shows after the title.
export function ItemMeta({ item }: { item: Item }): ReactElement {
  const t = parseTypeName(item.type);
  return (
    <span className="meta">
      {t.typeId}@{t.version} · {item.state} ({categoryName(item.category)})
    </span>
  );
}

export function ItemList({ items, label }: { items: Item[]; label: string }): ReactElement {
  const session = useSession();
  if (items.length === 0) return <p className="hint">No Items.</p>;
  return (
    <ul aria-label={label} className="items">
      {items.map((it) => (
        <li key={it.name}>
          <a href={`#/items/${itemId(it.name)}`}>{session.titleOf(it)}</a> <ItemMeta item={it} />
        </li>
      ))}
    </ul>
  );
}
