// An Item's links, both ways, with add and remove where the link type is
// not the kernel's to set.
//
// Phase B: signature only.
import type { ReactElement } from "react";
import type { Item, Link, LinkDirection, Type } from "../gen/kernel/v1/kernel_pb";

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
  search?: (words: string, typeNames: string[]) => Promise<Item[]>;
};

export function LinksPanel(_props: LinksPanelProps): ReactElement {
  throw new Error("not implemented");
}
