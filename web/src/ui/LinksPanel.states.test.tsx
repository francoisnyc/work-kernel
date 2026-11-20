// The links list shows each linked Item's state, where the page has read
// it (Jira part 2, step 3b, 8d: an incident's page shows the state of the
// bug it is linked to; no reaction carries it). Generic: any link of any
// Type, through visibility: a restricted end has no state to show.
import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import { aTicket, allTypes, itemName, ticket } from "../test/fixtures";
import { LinksPanel } from "./LinksPanel";

const ok = () => vi.fn().mockResolvedValue(undefined);

describe("the other Item's state", () => {
  test("is shown beside each link the page has read", () => {
    render(
      <LinksPanel
        item={aTicket}
        type={ticket}
        types={allTypes}
        onLink={ok()}
        onUnlink={ok()}
        states={{ [itemName(2)]: "in_progress", [itemName(3)]: "resolved" }}
      />,
    );
    const rows = screen.getAllByRole("listitem");
    expect(rows[0]).toHaveTextContent("blocks");
    expect(rows[0]).toHaveTextContent("in_progress");
    expect(rows[2]).toHaveTextContent("blocked_by");
    expect(rows[2]).toHaveTextContent("resolved");
    // The state is text beside the link, not part of the link's name: the
    // remove button and the link keep the names they had.
    expect(screen.getByRole("link", { name: /00000002/ })).not.toHaveTextContent("in_progress");
    expect(screen.getByRole("button", { name: "Remove blocks 00000002" })).toBeInTheDocument();
  });

  test("a restricted end shows no state, whatever the page holds", () => {
    render(
      <LinksPanel
        item={aTicket}
        type={ticket}
        types={allTypes}
        onLink={ok()}
        onUnlink={ok()}
        states={{ [itemName(2)]: "in_progress", "": "resolved" }}
      />,
    );
    const restricted = screen.getAllByRole("listitem")[1]!;
    expect(restricted).toHaveTextContent(/restricted/i);
    expect(restricted).not.toHaveTextContent("resolved");
    expect(restricted).not.toHaveTextContent("in_progress");
  });

  test("an Item the page has not read shows none, and nothing breaks without the prop", () => {
    const { unmount } = render(
      <LinksPanel item={aTicket} type={ticket} types={allTypes} onLink={ok()} onUnlink={ok()} states={{ [itemName(2)]: "open" }} />,
    );
    expect(screen.getAllByRole("listitem")[2]).not.toHaveTextContent(/open|resolved|in_progress/);
    unmount();
    render(<LinksPanel item={aTicket} type={ticket} types={allTypes} onLink={ok()} onUnlink={ok()} />);
    expect(screen.getAllByRole("listitem")).toHaveLength(3);
  });
});
