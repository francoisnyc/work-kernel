// The Inbox page (track 1 item 5, step 4, PR 2), against a fake kernel.
//
// What the page must do: ask WhoAmI for the tabs, CountItems for each
// tab's number, SearchItems with waiting_on for the rows; offer exactly
// the moves the server lists on each row; make each move with one
// TransitionItem and read the list and the numbers again. It decides
// nothing about who may act, and for a customer it makes no call at all.
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code } from "@connectrpc/connect";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import { Session, SessionProvider, type Api } from "../app/session";
import {
  CountItemsResponseSchema,
  FieldType,
  SearchItemsResponseSchema,
  StateCategory,
  TransitionItemResponseSchema,
  TransitionOutcome,
  TypeSchema,
  WhoAmIResponseSchema,
  type CountItemsRequestSchema,
  type Item,
  type SearchItemsRequestSchema,
  type TransitionItemRequestSchema,
} from "../gen/kernel/v1/kernel_pb";
import { NS, item, itemName, refusal, ticket, typeName } from "../test/fixtures";
import { InboxPage } from "./InboxPage";

// A purchase: finance takes it from a queue into a claimed review,
// decides, or gives it back to the queue.
const purchase = create(TypeSchema, {
  name: typeName("purchase", 1),
  typeId: "purchase",
  version: 1,
  fields: [
    { id: "f_title", type: FieldType.STRING },
    { id: "f_claimed_by", type: FieldType.PRINCIPAL },
  ],
  states: [
    { id: "finance_queue", category: StateCategory.WAITING },
    { id: "finance_review", category: StateCategory.ACTIVE },
    { id: "approved", category: StateCategory.DONE },
    { id: "rejected", category: StateCategory.CANCELED },
  ],
  transitions: [
    { fromState: "finance_queue", toState: "finance_review", allowed: ["role:finance"] },
    { fromState: "finance_review", toState: "approved", allowed: ["role:finance"] },
    { fromState: "finance_review", toState: "rejected", allowed: ["role:finance"] },
    { fromState: "finance_review", toState: "finance_queue", allowed: ["role:finance"], clears: ["f_claimed_by"] },
    { fromState: "approved", toState: "finance_review", allowed: ["role:cfo"] },
  ],
});

const hoursAgo = (h: number) => timestampFromDate(new Date(Date.now() - h * 3_600_000 - 5_000));

function queued(n: number, title: string, moves = ["finance_review"]): Item {
  return item({
    name: itemName(n),
    type: purchase.name,
    state: "finance_queue",
    category: StateCategory.WAITING,
    creator: "user:ana",
    fields: { f_title: title },
    stateTime: hoursAgo(n),
    moves,
  });
}

function claimed(n: number, title: string, by: string, moves = ["approved", "rejected", "finance_queue"]): Item {
  return item({
    name: itemName(n),
    type: purchase.name,
    state: "finance_review",
    category: StateCategory.ACTIVE,
    creator: "user:ana",
    fields: { f_title: title, f_claimed_by: by },
    stateTime: hoursAgo(n),
    moves,
  });
}

type SearchReq = MessageInitShape<typeof SearchItemsRequestSchema>;
type CountReq = MessageInitShape<typeof CountItemsRequestSchema>;
type MoveReq = MessageInitShape<typeof TransitionItemRequestSchema>;

// A fake kernel: `rows` and `count` say what each view holds; every call
// is recorded. Only the four calls the Inbox may make exist: any other
// call is a TypeError, which fails the test.
function kernel(opts: {
  roles?: string[];
  rows: (waitingOn: string, req: SearchReq) => { items: Item[]; nextPageToken?: string } | Promise<{ items: Item[]; nextPageToken?: string }>;
  count?: (waitingOn: string, req: CountReq) => { count: number; capped?: boolean };
  move?: (req: MoveReq) => void;
}) {
  const whoAmI = vi.fn(async () => create(WhoAmIResponseSchema, { principal: "user:fay", namespace: NS, roles: opts.roles ?? [] }));
  const countItems = vi.fn(async (req: CountReq) =>
    create(CountItemsResponseSchema, opts.count ? opts.count(req.filter?.waitingOn ?? "", req) : { count: 0 }),
  );
  const searchItems = vi.fn(async (req: SearchReq) => create(SearchItemsResponseSchema, await opts.rows(req.filter?.waitingOn ?? "", req)));
  const transitionItem = vi.fn(async (req: MoveReq) => {
    opts.move?.(req);
    return create(TransitionItemResponseSchema, { outcome: TransitionOutcome.APPLIED, commitSeq: 7n });
  });
  return { whoAmI, countItems, searchItems, transitionItem };
}

function show(k: ReturnType<typeof kernel>, principal = "user:fay") {
  const user = { name: principal.split(":")[1]!, token: "t", principal, namespace: "acme", roles: [] };
  const session = new Session(user, k as unknown as Api, [purchase, ticket], ["user:fay", "user:gus"]);
  return render(
    <SessionProvider value={session}>
      <InboxPage />
    </SessionProvider>,
  );
}

const tab = (name: RegExp) => screen.getByRole("tab", { name });
const rows = () => within(screen.getByRole("list", { name: "Inbox" })).queryAllByRole("link");
const titles = () => rows().map((a) => a.textContent);
const rowOf = (title: string) => screen.getByRole("link", { name: title }).closest("li")!;
const buttonsOf = (title: string) =>
  within(rowOf(title))
    .queryAllByRole("button")
    .map((b) => b.textContent);

test("a customer: one sentence, and no call at all", async () => {
  const k = kernel({ rows: () => ({ items: [] }) });
  show(k, "customer:kim");
  expect(screen.getByRole("heading", { name: "Inbox" })).toBeInTheDocument();
  expect(screen.getByText("Nothing waits on you here yet.")).toBeInTheDocument();
  expect(screen.queryByRole("tab")).not.toBeInTheDocument();
  await act(async () => {});
  for (const call of [k.whoAmI, k.countItems, k.searchItems, k.transitionItem]) expect(call).not.toHaveBeenCalled();
});

test("tabs: Waiting on me, then the caller's roles that a Type names, each with its count; 50+ when capped", async () => {
  // fay holds finance (purchase names it), approver (ticket names it) and
  // auditor (no Type names it: no tab).
  const k = kernel({
    roles: ["finance", "auditor", "approver"],
    rows: () => ({ items: [] }),
    count: (on) => ({ me: { count: 12 }, "role:finance": { count: 50, capped: true }, "role:approver": { count: 0 } })[on]!,
  });
  show(k);
  await waitFor(() => expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual(["Waiting on me 12", "approver 0", "finance 50+"]));
  expect(tab(/Waiting on me/)).toHaveAttribute("aria-selected", "true");
  expect(tab(/finance/)).toHaveAttribute("aria-selected", "false");
  expect(k.whoAmI).toHaveBeenCalledTimes(1);
  // One count per tab, each the tab's own filter and nothing else.
  expect(k.countItems.mock.calls.map(([req]) => req).sort((a, b) => (a.filter!.waitingOn! < b.filter!.waitingOn! ? -1 : 1))).toEqual([
    { parent: NS, filter: { waitingOn: "me" } },
    { parent: NS, filter: { waitingOn: "role:approver" } },
    { parent: NS, filter: { waitingOn: "role:finance" } },
  ]);
});

test("the rows: Waiting on me, oldest waiting first, a page of 50; the order switch and a role's tab change the one search", async () => {
  const k = kernel({
    roles: ["finance"],
    rows: (on, req) => {
      const items = on === "me" ? [claimed(9, "Spare parts", "user:fay"), claimed(2, "Chairs", "user:fay")] : [queued(5, "Laptops")];
      return { items: req.orderBy === "waiting_since desc" ? [...items].reverse() : items };
    },
  });
  show(k);
  await waitFor(() => expect(titles()).toEqual(["Spare parts", "Chairs"]));
  expect(k.searchItems).toHaveBeenLastCalledWith({ parent: NS, pageSize: 50, orderBy: "waiting_since", filter: { waitingOn: "me" } });

  await userEvent.selectOptions(screen.getByLabelText("Order"), "Newest waiting first");
  await waitFor(() => expect(titles()).toEqual(["Chairs", "Spare parts"]));
  expect(k.searchItems).toHaveBeenLastCalledWith({ parent: NS, pageSize: 50, orderBy: "waiting_since desc", filter: { waitingOn: "me" } });

  await userEvent.click(tab(/finance/));
  await waitFor(() => expect(titles()).toEqual(["Laptops"]));
  expect(tab(/finance/)).toHaveAttribute("aria-selected", "true");
  expect(k.searchItems).toHaveBeenLastCalledWith({
    parent: NS,
    pageSize: 50,
    orderBy: "waiting_since desc",
    filter: { waitingOn: "role:finance" },
  });
  // No search names a Type, a state or a field: the kernel works out what waits.
  for (const [req] of k.searchItems.mock.calls) expect(Object.keys(req.filter!)).toEqual(["waitingOn"]);
});

test("a row says who asked, how long it has waited and why it is here", async () => {
  const k = kernel({
    roles: ["finance"],
    rows: (on) => ({ items: on === "me" ? [claimed(5, "Spare parts", "user:fay"), queued(3, "Laptops")] : [queued(3, "Laptops")] }),
  });
  show(k);
  await waitFor(() => expect(titles()).toEqual(["Spare parts", "Laptops"]));
  expect(rowOf("Spare parts")).toHaveTextContent("purchase@1 · finance_review (active)");
  expect(rowOf("Spare parts")).toHaveTextContent("asked by user:ana · waiting 5 h · f_claimed_by is you");
  // In the queue and in fay's view because she is in finance: the browser
  // does not re-derive that, so it gives no reason.
  expect(rowOf("Laptops")).toHaveTextContent("asked by user:ana · waiting 3 h");
  expect(rowOf("Laptops")).not.toHaveTextContent(/is you|your team/);

  await userEvent.click(tab(/finance/));
  await waitFor(() => expect(titles()).toEqual(["Laptops"]));
  expect(rowOf("Laptops")).toHaveTextContent("asked by user:ana · waiting 3 h · for your team (finance)");
});

test("a row's buttons are exactly the server's moves, in its order; the Type's `allowed` decides none", async () => {
  const k = kernel({
    roles: ["finance"],
    rows: () => ({
      items: [
        claimed(1, "All three", "user:fay"),
        claimed(2, "Another order", "user:fay", ["finance_queue", "approved"]),
        // The Type would let finance decide this one; the server lists
        // nothing (someone else claimed it), so nothing is offered.
        claimed(3, "Not hers", "user:gus", []),
        // approved -> finance_review names role:cfo, which fay does not
        // hold; the server lists it, so it is offered.
        item({ name: itemName(4), type: purchase.name, state: "approved", fields: { f_title: "Reopened" }, moves: ["finance_review"] }),
      ],
    }),
  });
  show(k);
  await waitFor(() => expect(titles()).toHaveLength(4));
  expect(buttonsOf("All three")).toEqual(["Move to approved", "Move to rejected", "Move to finance_queue"]);
  expect(buttonsOf("Another order")).toEqual(["Move to finance_queue", "Move to approved"]);
  expect(buttonsOf("Not hers")).toEqual([]);
  expect(rowOf("Not hers")).toHaveTextContent("No move here is yours to make.");
  expect(buttonsOf("Reopened")).toEqual(["Move to finance_review"]);
});

test("a place: the Item's name goes in filter.in of the search and of every count", async () => {
  const k = kernel({ roles: ["finance"], rows: () => ({ items: [queued(1, "Laptops")] }) });
  show(k);
  await waitFor(() => expect(titles()).toEqual(["Laptops"]));
  k.countItems.mockClear();

  // A bare ID is an Item of the caller's namespace.
  const id = itemName(77).split("/").pop()!;
  await userEvent.type(screen.getByLabelText("Place"), id);
  await userEvent.click(screen.getByRole("button", { name: "Apply" }));
  await waitFor(() =>
    expect(k.searchItems).toHaveBeenLastCalledWith({
      parent: NS,
      pageSize: 50,
      orderBy: "waiting_since",
      filter: { waitingOn: "me", in: itemName(77) },
    }),
  );
  await waitFor(() => expect(k.countItems).toHaveBeenCalledTimes(2));
  for (const [req] of k.countItems.mock.calls) expect(req.filter!.in).toBe(itemName(77));

  // Emptied: anywhere again.
  await userEvent.clear(screen.getByLabelText("Place"));
  await userEvent.click(screen.getByRole("button", { name: "Apply" }));
  await waitFor(() =>
    expect(k.searchItems).toHaveBeenLastCalledWith({ parent: NS, pageSize: 50, orderBy: "waiting_since", filter: { waitingOn: "me" } }),
  );
});

test("More: the next page by its token, added below; no More on the last page", async () => {
  const first = Array.from({ length: 50 }, (_, i) => queued(100 - i, `Purchase ${i + 1}`));
  const k = kernel({
    rows: (_on, req) => (req.pageToken === "page-2" ? { items: [queued(1, "Purchase 51")] } : { items: first, nextPageToken: "page-2" }),
    count: () => ({ count: 50, capped: true }),
  });
  show(k);
  await waitFor(() => expect(titles()).toHaveLength(50));
  expect(tab(/Waiting on me/)).toHaveTextContent("50+");

  await userEvent.click(screen.getByRole("button", { name: "More" }));
  await waitFor(() => expect(titles()).toHaveLength(51));
  expect(titles()[0]).toBe("Purchase 1");
  expect(titles()[50]).toBe("Purchase 51");
  expect(k.searchItems).toHaveBeenLastCalledWith({
    parent: NS,
    pageSize: 50,
    orderBy: "waiting_since",
    filter: { waitingOn: "me" },
    pageToken: "page-2",
  });
  expect(screen.queryByRole("button", { name: "More" })).not.toBeInTheDocument();
});

test("nothing waiting: the page says so, on Waiting on me and on a role's tab", async () => {
  const k = kernel({ roles: ["finance"], rows: () => ({ items: [] }) });
  show(k);
  expect(await screen.findByText("Nothing is waiting on you.")).toBeInTheDocument();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "More" })).not.toBeInTheDocument();
  await userEvent.click(tab(/finance/));
  expect(await screen.findByText("Nothing is waiting on finance.")).toBeInTheDocument();
});

test("while a read is running the last rows stay, marked busy", async () => {
  let release!: () => void;
  const slow = new Promise<void>((r) => (release = r));
  const k = kernel({
    rows: async (_on, req) => {
      if (req.orderBy === "waiting_since desc") {
        await slow;
        return { items: [queued(2, "Chairs"), queued(1, "Laptops")] };
      }
      return { items: [queued(1, "Laptops"), queued(2, "Chairs")] };
    },
  });
  show(k);
  await waitFor(() => expect(titles()).toEqual(["Laptops", "Chairs"]));
  expect(screen.getByRole("list", { name: "Inbox" })).toHaveAttribute("aria-busy", "false");

  await userEvent.selectOptions(screen.getByLabelText("Order"), "Newest waiting first");
  await waitFor(() => expect(screen.getByRole("list", { name: "Inbox" })).toHaveAttribute("aria-busy", "true"));
  expect(titles()).toEqual(["Laptops", "Chairs"]);
  expect(screen.queryByText("Nothing is waiting on you.")).not.toBeInTheDocument();

  await act(async () => release());
  await waitFor(() => expect(titles()).toEqual(["Chairs", "Laptops"]));
  expect(screen.getByRole("list", { name: "Inbox" })).toHaveAttribute("aria-busy", "false");
});

test("a refusal is shown as it came, and the page does not claim the inbox is empty", async () => {
  const k = kernel({
    rows: () => {
      throw refusal(Code.NotFound, "NOT_FOUND");
    },
  });
  show(k);
  const alert = await screen.findByRole("alert");
  expect(alert).toHaveTextContent("NOT_FOUND");
  expect(screen.queryByText("Nothing is waiting on you.")).not.toBeInTheDocument();
});

test("claim, then release: each is one TransitionItem, and the rows and every count are read again", async () => {
  // The fake keeps one purchase and moves it as the kernel would.
  let state: "queue" | "fay" = "queue";
  const k = kernel({
    roles: ["finance"],
    rows: (on) => {
      if (on === "me") return { items: state === "fay" ? [claimed(1, "Laptops", "user:fay")] : [queued(1, "Laptops"), queued(2, "Chairs")] };
      // The team's queue holds what nobody has claimed; a released
      // purchase is the newest arrival, so the last when oldest is first.
      return { items: state === "queue" ? [queued(2, "Chairs"), queued(1, "Laptops")] : [queued(2, "Chairs")] };
    },
    count: (on) => ({ count: on === "me" ? (state === "fay" ? 1 : 2) : state === "queue" ? 2 : 1 }),
    move: (req) => {
      state = req.toState === "finance_review" ? "fay" : "queue";
    },
  });
  show(k);
  await userEvent.click(await screen.findByRole("tab", { name: /finance/ }));
  await waitFor(() => expect(titles()).toEqual(["Chairs", "Laptops"]));
  expect(tab(/finance/)).toHaveTextContent("finance 2");
  expect(buttonsOf("Laptops")).toEqual(["Move to finance_review"]);

  // Claim: the Type's own move into the claimed state.
  k.countItems.mockClear();
  await userEvent.click(within(rowOf("Laptops")).getByRole("button", { name: "Move to finance_review" }));
  await waitFor(() => expect(titles()).toEqual(["Chairs"]));
  expect(k.transitionItem).toHaveBeenCalledTimes(1);
  expect(k.transitionItem.mock.calls[0]![0]).toMatchObject({ name: itemName(1), toState: "finance_review", fields: {} });
  expect(k.transitionItem.mock.calls[0]![0].requestId).toMatch(/^[0-9a-f-]{36}$/);
  // The answer stays on the page though the row has left this tab.
  expect(screen.getByRole("status")).toHaveTextContent("Laptops: move to finance_review: APPLIED");
  // Both numbers were read again, and the next search asks for this write.
  await waitFor(() => expect(tab(/finance/)).toHaveTextContent("finance 1"));
  expect(tab(/Waiting on me/)).toHaveTextContent("Waiting on me 1");
  expect(k.countItems).toHaveBeenCalledTimes(2);
  expect(k.searchItems.mock.lastCall![0].minSeq).toBe(7n);

  // It is now in Waiting on me, with the claimant's moves.
  await userEvent.click(tab(/Waiting on me/));
  await waitFor(() => expect(titles()).toEqual(["Laptops"]));
  expect(buttonsOf("Laptops")).toEqual(["Move to approved", "Move to rejected", "Move to finance_queue"]);

  // Release: the Type's release move, a transition like any other.
  await userEvent.click(within(rowOf("Laptops")).getByRole("button", { name: "Move to finance_queue" }));
  await waitFor(() => expect(k.transitionItem).toHaveBeenCalledTimes(2));
  expect(k.transitionItem.mock.calls[1]![0]).toMatchObject({ name: itemName(1), toState: "finance_queue" });
  await waitFor(() => expect(tab(/finance/)).toHaveTextContent("finance 2"));
  await userEvent.click(tab(/finance/));
  // Back in the team's queue, at its end.
  await waitFor(() => expect(titles()).toEqual(["Chairs", "Laptops"]));
});

test("a refused move shows the refusal on its row; the row stays", async () => {
  const k = kernel({ roles: ["finance"], rows: () => ({ items: [queued(1, "Laptops")] }) });
  k.transitionItem.mockRejectedValueOnce(refusal(Code.PermissionDenied, "NOT_ALLOWED"));
  show(k);
  await waitFor(() => expect(titles()).toEqual(["Laptops"]));
  await userEvent.click(within(rowOf("Laptops")).getByRole("button", { name: "Move to finance_review" }));
  expect(await within(rowOf("Laptops")).findByRole("alert")).toHaveTextContent("NOT_ALLOWED");
  expect(titles()).toEqual(["Laptops"]);
});
