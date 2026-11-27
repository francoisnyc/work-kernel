// The Inbox for people, PR 2c: the side panel, several rows at once, the
// keys, against a fake kernel. The owner's decisions (PR 2's Phase A,
// 2026-11-27):
//
//   - the panel: a row's title (or Enter on it) opens it; an <aside>
//     named by the row's title, focus on its heading, back to the row's
//     title on close; the Type and the state's label, every field with its
//     label and help (read only), the moves (a required field asked for in
//     place), the history folded, a link to the full page; after a move
//     the rows and counts are read again, the panel goes to the next row,
//     and a status line gives the outcome in labels; #/inbox?item=<id>;
//   - several rows: a checkbox per row; the bar offers a move every
//     selected row lists, of one Type, with no required fields; one
//     TransitionItem per row, one after another; a result per row; failed
//     rows stay selected;
//   - keys on the list only: J/K between rows, Enter opens, X selects, P
//     makes the main move; never while typing; no listener on the window;
//     shown in a line under the list.
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code } from "@connectrpc/connect";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { Session, SessionProvider, type Api } from "../app/session";
import {
  CountItemsResponseSchema,
  FieldType,
  ItemEventKind,
  ListItemEventsResponseSchema,
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
import type { DevUser } from "../model/devusers";
import { itemId } from "../model/typeinfo";
import { NS, item, itemName, refusal, typeName } from "../test/fixtures";
import { InboxPage } from "./InboxPage";

const access = create(TypeSchema, {
  name: typeName("access", 2),
  typeId: "access",
  version: 2,
  summary: ["f_reason"],
  fields: [
    { id: "f_title", type: FieldType.STRING, label: "Request" },
    { id: "f_reason", type: FieldType.STRING, label: "Reason", help: "Why the access is needed" },
    { id: "f_approver", type: FieldType.PRINCIPAL, label: "Approver" },
    { id: "f_note", type: FieldType.STRING, label: "Note" },
  ],
  states: [
    { id: "pending", category: StateCategory.WAITING, label: "Waiting for approval" },
    { id: "approved", category: StateCategory.DONE, label: "Approved" },
    { id: "rejected", category: StateCategory.DONE, label: "Rejected" },
  ],
  transitions: [
    { fromState: "pending", toState: "approved", allowed: ["field:f_approver"], label: "Approve", primary: true },
    { fromState: "pending", toState: "rejected", allowed: ["field:f_approver"], required: ["f_note"], label: "Reject" },
  ],
});

const equipment = create(TypeSchema, {
  name: typeName("equipment", 2),
  typeId: "equipment",
  version: 2,
  fields: [
    { id: "f_title", type: FieldType.STRING, label: "What is needed" },
    { id: "f_handler", type: FieldType.PRINCIPAL, label: "Handled by" },
  ],
  states: [
    { id: "queue", category: StateCategory.WAITING, label: "In the queue" },
    { id: "in_hand", category: StateCategory.ACTIVE, label: "Being handled", claim: "f_handler" },
  ],
  transitions: [{ fromState: "queue", toState: "in_hand", allowed: ["role:desk_agent"], label: "Take it", primary: true }],
});

const people: DevUser[] = [
  { name: "ana", token: "a", principal: "user:ana", namespace: "acme", roles: [] },
  { name: "sam", token: "s", principal: "user:sam", namespace: "acme", roles: ["desk_agent"] },
];

const hoursAgo = (h: number) => timestampFromDate(new Date(Date.now() - h * 3_600_000 - 5_000));

function accessRow(n: number, title: string): Item {
  return item({
    name: itemName(n),
    type: access.name,
    state: "pending",
    category: StateCategory.WAITING,
    creator: "user:ana",
    fields: { f_title: title, f_reason: `reason ${n}`, f_approver: "user:sam" },
    stateTime: hoursAgo(10 - n),
    moves: ["approved", "rejected"],
  });
}

function queued(n: number, title: string, moves = ["in_hand"], type = equipment.name): Item {
  return item({
    name: itemName(n),
    type,
    state: "queue",
    category: StateCategory.WAITING,
    creator: "user:ana",
    fields: { f_title: title },
    stateTime: hoursAgo(10 - n),
    moves,
  });
}

type SearchReq = MessageInitShape<typeof SearchItemsRequestSchema>;
type CountReq = MessageInitShape<typeof CountItemsRequestSchema>;
type MoveReq = MessageInitShape<typeof TransitionItemRequestSchema>;

// A fake kernel: the Inbox's four calls, and ListItemEvents for a panel's
// folded history. `rows` answers each search; `move` may throw.
function kernel(opts: { roles?: string[]; rows: (req: SearchReq) => Item[]; move?: (req: MoveReq) => void | Promise<void> }) {
  return {
    whoAmI: vi.fn(async () => create(WhoAmIResponseSchema, { principal: "user:sam", namespace: NS, roles: opts.roles ?? [] })),
    countItems: vi.fn(async (_req: CountReq) => create(CountItemsResponseSchema, { count: 1 })),
    searchItems: vi.fn(async (req: SearchReq) => create(SearchItemsResponseSchema, { items: opts.rows(req) })),
    transitionItem: vi.fn(async (req: MoveReq) => {
      await opts.move?.(req);
      return create(TransitionItemResponseSchema, { outcome: TransitionOutcome.APPLIED, commitSeq: 3n });
    }),
    listItemEvents: vi.fn(async () =>
      create(ListItemEventsResponseSchema, {
        events: [{ name: `${itemName(1)}/events/1`, itemSeq: 1n, kind: ItemEventKind.CREATED, actor: "user:ana" }],
      }),
    ),
  };
}

function show(k: ReturnType<typeof kernel>) {
  const user = { name: "sam", token: "s", principal: "user:sam", namespace: "acme", roles: ["desk_agent"] };
  const session = new Session(user, k as unknown as Api, [access, equipment], ["user:sam", "user:ana"], people);
  return render(
    <SessionProvider value={session}>
      <InboxPage />
    </SessionProvider>,
  );
}

beforeEach(() => {
  window.location.hash = "#/inbox";
});
afterEach(() => {
  window.location.hash = "";
});

const inbox = () => screen.getByRole("region", { name: "Inbox" });
const titleLink = (title: string) => within(inbox()).getByRole("link", { name: title });
const rowOf = (title: string) => titleLink(title).closest("li")!;
const panel = () => screen.queryByRole("complementary");
const isTeam = (req: SearchReq) => req.filter?.waitingOn === "role:desk_agent";

// --- The panel ---

test("a row's title opens its panel: an aside named by the title, the Type and state, every field with its label and help, a link to the full page", async () => {
  const k = kernel({ rows: (req) => (isTeam(req) ? [] : [accessRow(1, "Finance share"), accessRow(2, "HR share")]) });
  show(k);
  await userEvent.click(await screen.findByRole("link", { name: "Finance share" }));
  const aside = await screen.findByRole("complementary", { name: "Finance share" });
  expect(within(aside).getByRole("heading", { name: "Finance share" })).toBeInTheDocument();
  expect(aside).toHaveTextContent("access · Waiting for approval");
  // Every field, by its label, with its value; help under it; read only.
  const fields = within(aside).getByRole("list", { name: "Fields" });
  expect(fields).toHaveTextContent("Request");
  expect(fields).toHaveTextContent("Reason");
  expect(fields).toHaveTextContent("reason 1");
  expect(fields).toHaveTextContent("Why the access is needed");
  expect(fields).toHaveTextContent("Approver");
  expect(fields).toHaveTextContent("Sam");
  expect(fields).toHaveTextContent("Note");
  expect(within(fields).queryAllByRole("textbox")).toEqual([]);
  // The full page.
  expect(within(aside).getByRole("link", { name: "Open the full page" })).toHaveAttribute("href", `#/items/${itemId(itemName(1))}`);
  // Its moves: all of Item.moves, by label.
  expect(within(aside).getByRole("button", { name: "Approve" })).toBeInTheDocument();
  expect(within(aside).getByRole("button", { name: "Reject" })).toBeInTheDocument();
  // The URL keeps it.
  expect(window.location.hash).toBe(`#/inbox?item=${itemId(itemName(1))}`);
});

test("the panel's history is folded, and read only when unfolded", async () => {
  const k = kernel({ rows: (req) => (isTeam(req) ? [] : [accessRow(1, "Finance share")]) });
  show(k);
  await userEvent.click(await screen.findByRole("link", { name: "Finance share" }));
  const aside = await screen.findByRole("complementary", { name: "Finance share" });
  const history = within(aside).getByText("History").closest("details")!;
  expect(history).not.toHaveAttribute("open");
  expect(k.listItemEvents).not.toHaveBeenCalled();
  await userEvent.click(within(aside).getByText("History"));
  await waitFor(() => expect(k.listItemEvents).toHaveBeenCalledWith(expect.objectContaining({ parent: itemName(1) })));
  expect(await within(history).findByText(/created/)).toBeInTheDocument();
});

test("focus goes to the panel's heading on open, and back to the row's title on close; the URL forgets it", async () => {
  const k = kernel({ rows: (req) => (isTeam(req) ? [] : [accessRow(1, "Finance share")]) });
  show(k);
  await userEvent.click(await screen.findByRole("link", { name: "Finance share" }));
  const aside = await screen.findByRole("complementary", { name: "Finance share" });
  await waitFor(() => expect(within(aside).getByRole("heading", { name: "Finance share" })).toHaveFocus());
  await userEvent.click(within(aside).getByRole("button", { name: "Close" }));
  await waitFor(() => expect(panel()).not.toBeInTheDocument());
  expect(titleLink("Finance share")).toHaveFocus();
  expect(window.location.hash).toBe("#/inbox");
});

test("#/inbox?item=<id> opens that row's panel once the rows are read", async () => {
  window.location.hash = `#/inbox?item=${itemId(itemName(2))}`;
  const k = kernel({ rows: (req) => (isTeam(req) ? [] : [accessRow(1, "Finance share"), accessRow(2, "HR share")]) });
  show(k);
  expect(await screen.findByRole("complementary", { name: "HR share" })).toBeInTheDocument();
});

test("a move with a required field, from the panel: asked in place, then one TransitionItem; the panel goes to the next row and the outcome is said in labels", async () => {
  let gone = false;
  const k = kernel({
    rows: (req) => (isTeam(req) ? [] : gone ? [accessRow(2, "HR share")] : [accessRow(1, "Finance share"), accessRow(2, "HR share")]),
    move: () => {
      gone = true;
    },
  });
  show(k);
  await userEvent.click(await screen.findByRole("link", { name: "Finance share" }));
  const aside = await screen.findByRole("complementary", { name: "Finance share" });
  await userEvent.click(within(aside).getByRole("button", { name: "Reject" }));
  expect(k.transitionItem).not.toHaveBeenCalled();
  await userEvent.type(within(aside).getByLabelText(/f_note|Note/), "not this quarter");
  await userEvent.click(within(aside).getByRole("button", { name: /confirm/i }));
  await waitFor(() => expect(k.transitionItem).toHaveBeenCalledTimes(1));
  expect(k.transitionItem.mock.calls[0]![0]).toMatchObject({ name: itemName(1), toState: "rejected", fields: { f_note: "not this quarter" } });
  // The rows and the counts were read again; the panel is on the next row.
  expect(await screen.findByRole("complementary", { name: "HR share" })).toBeInTheDocument();
  expect(screen.getByRole("status")).toHaveTextContent("Finance share: Reject: APPLIED");
  expect(window.location.hash).toBe(`#/inbox?item=${itemId(itemName(2))}`);
  expect(k.countItems.mock.calls.length).toBeGreaterThanOrEqual(2);
});

// --- Several rows at once ---

test("a checkbox per row; the bar offers what every selected row lists, of one Type, with no required fields", async () => {
  const k = kernel({
    roles: ["desk_agent"],
    rows: (req) => (isTeam(req) && !req.filter?.claimed ? [queued(1, "Monitor"), queued(2, "Headset"), accessRow(3, "Odd one")] : []),
  });
  show(k);
  await userEvent.click(await screen.findByRole("tab", { name: /Desk agent/ }));
  await userEvent.click(await screen.findByRole("checkbox", { name: "Select Monitor" }));
  const bar = screen.getByRole("region", { name: "Selected rows" });
  expect(bar).toHaveTextContent("1 selected");
  expect(within(bar).getByRole("button", { name: "Take it (1)" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("checkbox", { name: "Select Headset" }));
  expect(bar).toHaveTextContent("2 selected");
  expect(within(bar).getAllByRole("button").map((b) => b.textContent)).toEqual(["Take it (2)", "Clear"]);
  // A row of another Type: nothing in common to offer.
  await userEvent.click(screen.getByRole("checkbox", { name: "Select Odd one" }));
  expect(within(bar).queryByRole("button", { name: /Take it/ })).not.toBeInTheDocument();
  await userEvent.click(within(bar).getByRole("button", { name: "Clear" }));
  expect(screen.queryByRole("region", { name: "Selected rows" })).not.toBeInTheDocument();
  expect(screen.getByRole("checkbox", { name: "Select Monitor" })).not.toBeChecked();
});

test("the bar's move: one TransitionItem per row, one after another; a result per row; failed rows stay selected", async () => {
  let first!: () => void;
  const held = new Promise<void>((r) => (first = r));
  const k = kernel({
    roles: ["desk_agent"],
    rows: (req) => (isTeam(req) && !req.filter?.claimed ? [queued(1, "Monitor"), queued(2, "Headset"), queued(3, "Dock")] : []),
    move: async (req) => {
      if (req.name === itemName(1)) await held;
      if (req.name === itemName(2)) throw refusal(Code.PermissionDenied, "NOT_ALLOWED");
    },
  });
  show(k);
  await userEvent.click(await screen.findByRole("tab", { name: /Desk agent/ }));
  for (const t of ["Monitor", "Headset", "Dock"]) await userEvent.click(await screen.findByRole("checkbox", { name: `Select ${t}` }));
  await userEvent.click(screen.getByRole("button", { name: "Take it (3)" }));
  // One after another: the second waits for the first's answer.
  await waitFor(() => expect(k.transitionItem).toHaveBeenCalledTimes(1));
  await new Promise((r) => setTimeout(r, 30));
  expect(k.transitionItem).toHaveBeenCalledTimes(1);
  await act(async () => first());
  await waitFor(() => expect(k.transitionItem).toHaveBeenCalledTimes(3));
  expect(k.transitionItem.mock.calls.map(([r]) => [r.name, r.toState])).toEqual([
    [itemName(1), "in_hand"],
    [itemName(2), "in_hand"],
    [itemName(3), "in_hand"],
  ]);
  const results = await screen.findByRole("list", { name: "Results" });
  await waitFor(() =>
    expect(within(results).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "Monitor: Take it: APPLIED",
      "Headset: NOT_ALLOWED",
      "Dock: Take it: APPLIED",
    ]),
  );
  // The failed row stays selected; the others do not.
  expect(screen.getByRole("checkbox", { name: "Select Headset" })).toBeChecked();
  expect(screen.getByRole("checkbox", { name: "Select Monitor" })).not.toBeChecked();
  expect(screen.getByRole("region", { name: "Selected rows" })).toHaveTextContent("1 selected");
});

// --- Keys ---

test("J and K move between rows, Enter opens the panel, X selects, P makes the main move", async () => {
  const k = kernel({ rows: (req) => (isTeam(req) ? [] : [accessRow(1, "Finance share"), accessRow(2, "HR share"), accessRow(3, "Legal share")]) });
  show(k);
  titleLink((await screen.findByRole("link", { name: "Finance share" })).textContent!).focus();
  await userEvent.keyboard("j");
  expect(titleLink("HR share")).toHaveFocus();
  await userEvent.keyboard("j");
  expect(titleLink("Legal share")).toHaveFocus();
  await userEvent.keyboard("j"); // the last row: stays
  expect(titleLink("Legal share")).toHaveFocus();
  await userEvent.keyboard("k");
  expect(titleLink("HR share")).toHaveFocus();

  await userEvent.keyboard("x");
  expect(within(rowOf("HR share")).getByRole("checkbox")).toBeChecked();

  await userEvent.keyboard("p");
  await waitFor(() => expect(k.transitionItem).toHaveBeenCalledTimes(1));
  expect(k.transitionItem.mock.calls[0]![0]).toMatchObject({ name: itemName(2), toState: "approved" });

  titleLink("Finance share").focus();
  await userEvent.keyboard("{Enter}");
  expect(await screen.findByRole("complementary", { name: "Finance share" })).toBeInTheDocument();
});

test("the keys are said under the list", async () => {
  const k = kernel({ rows: (req) => (isTeam(req) ? [] : [accessRow(1, "Finance share")]) });
  show(k);
  await screen.findByRole("link", { name: "Finance share" });
  expect(screen.getByText("Keys: J and K to move · Enter to open · X to select · P for the main move")).toBeInTheDocument();
});

test("never while typing: J in the Place field types a j and moves nothing", async () => {
  const k = kernel({ rows: (req) => (isTeam(req) ? [] : [accessRow(1, "Finance share"), accessRow(2, "HR share")]) });
  show(k);
  await screen.findByRole("link", { name: "Finance share" });
  const placeField = screen.getByLabelText("Place");
  await userEvent.click(placeField);
  await userEvent.keyboard("jpx");
  expect(placeField).toHaveValue("jpx");
  expect(placeField).toHaveFocus();
  expect(k.transitionItem).not.toHaveBeenCalled();
});

test("no key listener on the window or the document: the list alone listens", async () => {
  const onWindow = vi.spyOn(window, "addEventListener");
  const onDocument = vi.spyOn(document, "addEventListener");
  const k = kernel({ rows: (req) => (isTeam(req) ? [] : [accessRow(1, "Finance share")]) });
  show(k);
  await screen.findByRole("link", { name: "Finance share" });
  for (const spy of [onWindow, onDocument]) {
    expect(spy.mock.calls.map(([type]) => type)).not.toContain("keydown");
    expect(spy.mock.calls.map(([type]) => type)).not.toContain("keyup");
    expect(spy.mock.calls.map(([type]) => type)).not.toContain("keypress");
  }
  onWindow.mockRestore();
  onDocument.mockRestore();
});
