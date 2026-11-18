// Links both ways; add and remove only where the link type is not the
// kernel's to set.
import { Code } from "@connectrpc/connect";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { LinkDirection } from "../gen/kernel/v1/kernel_pb";
import {
  aSubmittedVacation,
  aTicket,
  allTypes,
  item,
  itemName,
  leaveBalance,
  refusal,
  sprint,
  ticket,
  vacation,
} from "../test/fixtures";
import { LinksPanel } from "./LinksPanel";

const ok = () => vi.fn().mockResolvedValue(undefined);

describe("the list", () => {
  test("shows each link by its name as this Item sees it, linking to the other Item", () => {
    render(<LinksPanel item={aTicket} type={ticket} types={allTypes} onLink={ok()} onUnlink={ok()} />);
    const rows = screen.getAllByRole("listitem");
    expect(rows).toHaveLength(3);
    expect(rows[0]).toHaveTextContent("blocks");
    expect(rows[2]).toHaveTextContent("blocked_by");
    const other = screen.getByRole("link", { name: /00000002/ });
    expect(other).toHaveAttribute("href", `#/items/${itemName(2).split("/").pop()}`);
  });

  test("a restricted end is shown as such, with no link and no remove", () => {
    render(<LinksPanel item={aTicket} type={ticket} types={allTypes} onLink={ok()} onUnlink={ok()} />);
    const restricted = screen.getAllByRole("listitem")[1]!;
    expect(restricted).toHaveTextContent(/restricted/i);
    expect(restricted.querySelector("a")).toBeNull();
    expect(restricted.querySelector("button")).toBeNull();
  });

  test("says when there are more links than shown", () => {
    render(
      <LinksPanel item={item({ ...aTicket, linksTruncated: true })} type={ticket} types={allTypes} onLink={ok()} onUnlink={ok()} />,
    );
    expect(screen.getByText(/more links exist/i)).toBeInTheDocument();
  });

  test("an Item with no link says so", () => {
    const lone = item({ name: itemName(8), type: ticket.name, state: "triage" });
    render(<LinksPanel item={lone} type={ticket} types={allTypes} onLink={ok()} onUnlink={ok()} />);
    expect(screen.queryAllByRole("listitem")).toHaveLength(0);
    expect(screen.getByText(/no links/i)).toBeInTheDocument();
  });
});

describe("remove", () => {
  test("an outgoing and an incoming link can each be removed", async () => {
    const onUnlink = ok();
    render(<LinksPanel item={aTicket} type={ticket} types={allTypes} onLink={ok()} onUnlink={onUnlink} />);
    expect(screen.getAllByRole("button", { name: /^Remove / })).toHaveLength(2);
    await userEvent.click(screen.getByRole("button", { name: "Remove blocks 00000002" }));
    expect(onUnlink).toHaveBeenCalledWith(aTicket.links[0]);
    await userEvent.click(screen.getByRole("button", { name: "Remove blocked_by 00000003" }));
    expect(onUnlink).toHaveBeenLastCalledWith(aTicket.links[2]);
  });

  test("a link the kernel sets has no remove and says who sets it", () => {
    render(<LinksPanel item={aSubmittedVacation} type={vacation} types={allTypes} onLink={ok()} onUnlink={ok()} />);
    expect(screen.getByRole("listitem")).toHaveTextContent("balance");
    expect(screen.getByRole("listitem")).toHaveTextContent(/set by the kernel/i);
    expect(screen.queryByRole("button", { name: /^Remove / })).not.toBeInTheDocument();
  });

  test("a refused remove shows the refusal", async () => {
    const onUnlink = vi.fn().mockRejectedValue(refusal(Code.FailedPrecondition, "NOT_LINKED"));
    render(<LinksPanel item={aTicket} type={ticket} types={allTypes} onLink={ok()} onUnlink={onUnlink} />);
    await userEvent.click(screen.getByRole("button", { name: "Remove blocks 00000002" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("NOT_LINKED");
  });
});

describe("add", () => {
  test("one add per link type the Type declares, and per link other Types declare towards it", () => {
    render(<LinksPanel item={aTicket} type={ticket} types={allTypes} onLink={ok()} onUnlink={ok()} />);
    expect(screen.getAllByRole("button", { name: /^Add / }).map((b) => b.textContent)).toEqual([
      "Add blocks",
      "Add duplicate_of",
      "Add blocked_by",
      "Add duplicates",
      "Add watched_by",
    ]);
  });

  test("adds an outgoing link to a pasted Item name", async () => {
    const onLink = ok();
    render(<LinksPanel item={aTicket} type={ticket} types={allTypes} onLink={onLink} onUnlink={ok()} />);
    await userEvent.type(screen.getByLabelText("Item to add as blocks"), itemName(9));
    await userEvent.click(screen.getByRole("button", { name: "Add blocks" }));
    expect(onLink).toHaveBeenCalledWith(LinkDirection.OUTGOING, "blocks", itemName(9));
  });

  test("adds an incoming link from the target's side: an issue to a sprint", async () => {
    const onLink = ok();
    const aSprint = item({ name: itemName(20), type: sprint.name, state: "planned" });
    render(<LinksPanel item={aSprint} type={sprint} types={allTypes} onLink={onLink} onUnlink={ok()} />);
    await userEvent.type(screen.getByLabelText("Item to add as sprint_issues"), itemName(21));
    await userEvent.click(screen.getByRole("button", { name: "Add sprint_issues" }));
    expect(onLink).toHaveBeenCalledWith(LinkDirection.INCOMING, "sprint_issues", itemName(21));
  });

  test("nothing to add without an Item", async () => {
    const onLink = ok();
    render(<LinksPanel item={aTicket} type={ticket} types={allTypes} onLink={onLink} onUnlink={ok()} />);
    await userEvent.click(screen.getByRole("button", { name: "Add blocks" }));
    expect(onLink).not.toHaveBeenCalled();
  });

  test("no add for a link the kernel sets, from either end", () => {
    render(<LinksPanel item={aSubmittedVacation} type={vacation} types={allTypes} onLink={ok()} onUnlink={ok()} />);
    expect(screen.queryByRole("button", { name: "Add balance" })).not.toBeInTheDocument();
    const balance = item({ name: itemName(11), type: leaveBalance.name, state: "open" });
    render(<LinksPanel item={balance} type={leaveBalance} types={allTypes} onLink={ok()} onUnlink={ok()} />);
    expect(screen.queryByRole("button", { name: "Add requests" })).not.toBeInTheDocument();
  });

  test("a refused add shows the refusal with its metadata", async () => {
    const onLink = vi.fn().mockRejectedValue(refusal(Code.FailedPrecondition, "LINK_CARDINALITY", { link_type: "duplicate_of", side: "source" }));
    render(<LinksPanel item={aTicket} type={ticket} types={allTypes} onLink={onLink} onUnlink={ok()} />);
    await userEvent.type(screen.getByLabelText("Item to add as duplicate_of"), itemName(9));
    await userEvent.click(screen.getByRole("button", { name: "Add duplicate_of" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("LINK_CARDINALITY");
    expect(alert).toHaveTextContent("link_type: duplicate_of");
  });
});
