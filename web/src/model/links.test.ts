import { describe, expect, test } from "vitest";
import { LinkDirection } from "../gen/kernel/v1/kernel_pb";
import {
  aSubmittedVacation,
  aTicket,
  allTypes,
  item,
  itemName,
  leaveBalance,
  sprint,
  story,
  ticket,
  vacation,
} from "../test/fixtures";
import { linkRows, linkWrite } from "./links";

describe("linkWrite", () => {
  test("an outgoing link is written on the Item itself", () => {
    expect(linkWrite(aTicket, LinkDirection.OUTGOING, "blocks", itemName(2), ticket)).toEqual({
      name: aTicket.name,
      linkType: "blocks",
      target: itemName(2),
    });
  });

  test("an incoming link is written on the other Item, by its declared name", () => {
    // "Add an issue to this sprint": the story is the source of `sprint`.
    const aSprint = item({ name: itemName(20), type: sprint.name, state: "planned" });
    expect(linkWrite(aSprint, LinkDirection.INCOMING, "sprint_issues", itemName(21), story)).toEqual({
      name: itemName(21),
      linkType: "sprint",
      target: itemName(20),
    });
    expect(linkWrite(aTicket, LinkDirection.INCOMING, "blocked_by", itemName(3), ticket)).toEqual({
      name: itemName(3),
      linkType: "blocks",
      target: aTicket.name,
    });
  });

  test("the other Item's Type declares no such link: an error, not a guess", () => {
    const aSprint = item({ name: itemName(20), type: sprint.name, state: "planned" });
    expect(() => linkWrite(aSprint, LinkDirection.INCOMING, "sprint_issues", itemName(1), ticket)).toThrow(/declares no link/i);
  });
});

describe("linkRows", () => {
  test("links the user may write can be removed, from either end", () => {
    const rows = linkRows(aTicket, ticket, allTypes);
    expect(rows.map((r) => [r.link.linkType, r.restricted, r.kernel, r.removable])).toEqual([
      ["blocks", false, false, true],
      ["blocks", true, false, false], // restricted: no name to remove it by
      ["blocked_by", false, false, true],
    ]);
  });

  test("a link only the kernel sets cannot be removed, from either end", () => {
    expect(linkRows(aSubmittedVacation, vacation, allTypes)).toMatchObject([{ kernel: true, removable: false }]);
    const balance = item({
      name: itemName(11),
      type: leaveBalance.name,
      state: "open",
      links: [{ linkType: "requests", direction: LinkDirection.INCOMING, item: { name: itemName(10) } }],
    });
    expect(linkRows(balance, leaveBalance, allTypes)).toMatchObject([{ kernel: true, removable: false }]);
  });
});
