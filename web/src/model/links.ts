// Link writes. LinkItems and UnlinkItems are always called on the link's
// source with the declared name; the page shows links from either end.
//
// Phase B: signatures only.
import type { Item, Link, LinkDirection, Type } from "../gen/kernel/v1/kernel_pb";

// What LinkItems or UnlinkItems is called with.
export type LinkWrite = { name: string; linkType: string; target: string };

// The write that adds or removes a link between `item` and `other`, named
// as `item` sees it: an outgoing link by its declared name, from item; an
// incoming one by its inverse, from other, under the name that
// sourceType (other's Type) declares for that inverse. Throws when
// sourceType declares no such link.
export function linkWrite(
  _item: Item,
  _direction: LinkDirection,
  _linkType: string,
  _other: string,
  _sourceType: Type,
): LinkWrite {
  throw new Error("not implemented");
}

// What a row of the links list may do. A restricted end has no name, so it
// cannot be removed; a link only the kernel sets has no add or remove.
export type LinkRow = { link: Link; restricted: boolean; kernel: boolean; removable: boolean };

// The Item's links as rows. An incoming link's writability is read from
// the Types that declare it (types: every Type, from ListTypes).
export function linkRows(_item: Item, _type: Type, _types: Type[]): LinkRow[] {
  throw new Error("not implemented");
}
