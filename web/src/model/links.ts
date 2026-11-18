// Link writes. LinkItems and UnlinkItems are always called on the link's
// source with the declared name; the page shows links from either end.
import { FieldWriter, LinkDirection, type Item, type Link, type Type } from "../gen/kernel/v1/kernel_pb";
import { incomingLinkTypes } from "./typeinfo";

// What LinkItems or UnlinkItems is called with.
export type LinkWrite = { name: string; linkType: string; target: string };

// The write that adds or removes a link between `item` and `other`, named
// as `item` sees it: an outgoing link by its declared name, from item; an
// incoming one by its inverse, from other, under the name that
// sourceType (other's Type) declares for that inverse. Throws when
// sourceType declares no such link.
export function linkWrite(
  item: Item,
  direction: LinkDirection,
  linkType: string,
  other: string,
  sourceType: Type,
): LinkWrite {
  if (direction !== LinkDirection.INCOMING) return { name: item.name, linkType, target: other };
  const declared = sourceType.linkTypes.find((lt) => lt.inverse === linkType);
  if (!declared) throw new Error(`${sourceType.typeId} declares no link whose inverse is ${linkType}`);
  return { name: other, linkType: declared.name, target: item.name };
}

// What a row of the links list may do. A restricted end has no name, so it
// cannot be removed; a link only the kernel sets has no add or remove.
export type LinkRow = { link: Link; restricted: boolean; kernel: boolean; removable: boolean };

// The Item's links as rows. An incoming link's writability is read from
// the Types that declare it (types: every Type, from ListTypes).
export function linkRows(item: Item, type: Type, types: Type[]): LinkRow[] {
  const incoming = incomingLinkTypes(types, type.typeId);
  return item.links.map((link) => {
    const restricted = link.item?.restricted === true;
    const kernel =
      link.direction === LinkDirection.INCOMING
        ? incoming.some((l) => l.name === link.linkType && !l.writable)
        : type.linkTypes.find((lt) => lt.name === link.linkType)?.writableBy === FieldWriter.KERNEL;
    return { link, restricted, kernel, removable: !restricted && !kernel };
  });
}
