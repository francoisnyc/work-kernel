// A rank move through the public API, as `pli rank` makes it: rank.plan
// over a few ordered reads of the moved card's CONTAINER (ListItems), then
// one ordinary UpdateItem (several only when the neighbours shared a key).
// Ranking against the container, not the board, keeps the container's
// order: the card sits next to the other one in every view of it.
import type { Api } from "../app/session";
import type { Item } from "../gen/kernel/v1/kernel_pb";
import { plan, type Card, type Position, type Query, type Source, type Write } from "../rank/move";
import { RankError, needsRebalance } from "../rank/rank";

const keyOf = (item: Item, field: string): string => {
  const v = item.fields?.[field];
  return typeof v === "string" ? v : "";
};

// One container's ranked cards, read with a field condition and order_by.
function container(api: Api, parent: string, field: string): Source {
  return {
    async scan(q: Query): Promise<Card[]> {
      const bound = q.eq ? { eq: q.eq } : q.gt ? { gt: q.gt } : q.lt ? { lt: q.lt } : undefined;
      const value = (v: string) => ({ kind: { case: "stringValue" as const, value: v } });
      const res = await api.listItems({
        parent,
        orderBy: q.desc ? `${field} desc` : field,
        pageSize: q.limit,
        filter: bound
          ? { fields: [{ field, ...Object.fromEntries(Object.entries(bound).map(([op, v]) => [op, value(v)])) }] }
          : undefined,
      });
      // Items without a rank come last in either order; they are not cards.
      return res.items.map((it) => ({ id: it.name, key: keyOf(it, field) })).filter((c) => c.key !== "");
    },
  };
}

export type RankMoveResult = {
  writes: number;
  // A key written is past the threshold: the container needs a rebalance.
  // The move itself succeeded; a move never rebalances.
  needsRebalance: boolean;
  commitSeq: bigint;
};

// Moves `moved` next to `other` (or to the bottom of its container) in the
// field `field`. Throws RankError (no_rank, tied_run_too_long, or changed
// when the cards kept moving), or the server's refusal of a write.
export async function rankMove(
  api: Api,
  ns: string,
  moved: Item,
  pos: Position,
  otherName: string | undefined,
  field: string,
): Promise<RankMoveResult> {
  const parent = moved.container !== "" ? moved.container : `namespaces/${ns}`;
  const src = container(api, parent, field);
  // The plan's reads are not one snapshot: if a card moved in between,
  // read the other card again and plan again, a few times.
  let writes: Write[] = [];
  for (let attempt = 0; ; attempt++) {
    let other: Card | undefined;
    if (otherName) {
      const o = (await api.getItem({ name: otherName })).item!;
      other = { id: o.name, key: keyOf(o, field) };
    }
    try {
      writes = await plan(src, moved.name, pos, other);
      break;
    } catch (e) {
      if (!(e instanceof RankError && e.code === "changed") || attempt === 3) throw e;
    }
  }
  // The moved card's write last, as pli does.
  writes.sort((a, b) => Number(a.id === moved.name) - Number(b.id === moved.name));
  let commitSeq = 0n;
  for (const w of writes) {
    const res = await api.updateItem({ name: w.id, requestId: crypto.randomUUID(), fields: { [field]: w.key } });
    commitSeq = res.commitSeq;
  }
  return { writes: writes.length, needsRebalance: writes.some((w) => needsRebalance(w.key)), commitSeq };
}
