// A rank move through the public API, as `pli rank` and web/ make it:
// rank.plan (web/'s port, read-only) over a few ordered reads of the moved
// card's CONTAINER, then one ordinary UpdateItem of f_rank (several only
// when the neighbours shared a key). Ranking against the container keeps
// the card next to the other one in every view of it.
import type { Item } from "@gen/kernel/v1/kernel_pb";
import { plan, type Card, type Position, type Query, type Source, type Write } from "@rank/move";
import { RankError, needsRebalance } from "@rank/rank";
import type { Session } from "./session";

const FIELD = "f_rank";
const keyOf = (item: Item | undefined): string => (typeof item?.fields?.[FIELD] === "string" ? (item.fields[FIELD] as string) : "");

function container(s: Session, parent: string): Source {
  return {
    async scan(q: Query): Promise<Card[]> {
      const value = (v: string) => ({ kind: { case: "stringValue" as const, value: v } });
      const bound = q.eq !== undefined ? { eq: value(q.eq) } : q.gt !== undefined ? { gt: value(q.gt) } : q.lt !== undefined ? { lt: value(q.lt) } : undefined;
      const res = await s.api.listItems({
        parent,
        orderBy: q.desc ? `${FIELD} desc` : FIELD,
        pageSize: q.limit,
        filter: bound ? { fields: [{ field: FIELD, ...bound }] } : undefined,
      });
      // Items without a rank come last in either order; they are not cards.
      return res.items.map((it) => ({ id: it.name, key: keyOf(it) })).filter((c) => c.key !== "");
    },
  };
}

// Moves `moved` next to `other` (or to the bottom of its container).
// Returns whether a key written is past the threshold: the container needs
// a rebalance (pli rank rebalance); the move itself succeeded.
export async function rankMove(s: Session, moved: Item, pos: Position, otherName?: string): Promise<{ needsRebalance: boolean }> {
  const src = container(s, moved.container !== "" ? moved.container : s.ns);
  let writes: Write[] = [];
  // The plan's reads are not one snapshot: if a card moved in between,
  // read the other card again and plan again, a few times.
  for (let attempt = 0; ; attempt++) {
    const other = otherName ? { id: otherName, key: keyOf((await s.api.getItem({ name: otherName })).item) } : undefined;
    try {
      writes = await plan(src, moved.name, pos, other);
      break;
    } catch (e) {
      if (!(e instanceof RankError && e.code === "changed") || attempt === 3) throw e;
    }
  }
  writes.sort((a, b) => Number(a.id === moved.name) - Number(b.id === moved.name));
  for (const w of writes) {
    s.wrote(await s.api.updateItem({ name: w.id, requestId: crypto.randomUUID(), fields: { [FIELD]: w.key } }));
  }
  return { needsRebalance: writes.some((w) => needsRebalance(w.key)) };
}
