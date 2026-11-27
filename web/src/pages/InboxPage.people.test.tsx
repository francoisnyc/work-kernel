// The Inbox for people, PR 2b: rows, groups and team tabs, against a fake
// kernel. The owner's decisions (Phase A, 2026-11-27):
//
//   - a row: its title, the Type's summary fields as "Label: value", its
//     Type and state, who asked (a name), why it is yours (in labels), how
//     long it waited, and when it is due, in amber with a word once past;
//   - its main button: the move marked primary when the server lists it,
//     else the first it lists; the others behind "More moves", a real
//     button with aria-expanded;
//   - Waiting on me: Overdue, then Waiting, on the rows loaded;
//   - a team tab: "Nobody has these yet" (the queue), then "Being
//     handled" (filter.claimed), each paged on its own; the tab's number
//     is the queue's; a claimed row names who has it; a withheld move says
//     why;
//   - names from the dev-token file, else the ID; a role's ID made
//     readable for its tab.
//
// The page still decides nothing about who may act: the buttons are
// Item.moves, the reasons Item.withheld.
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
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
  WithheldReason,
  type CountItemsRequestSchema,
  type Item,
  type SearchItemsRequestSchema,
  type TransitionItemRequestSchema,
} from "../gen/kernel/v1/kernel_pb";
import type { DevUser } from "../model/devusers";
import { NS, item, itemName, typeName } from "../test/fixtures";
import { InboxPage } from "./InboxPage";

const access = create(TypeSchema, {
  name: typeName("access", 2),
  typeId: "access",
  version: 2,
  summary: ["f_reason"],
  fields: [
    { id: "f_title", type: FieldType.STRING, label: "Request" },
    { id: "f_reason", type: FieldType.STRING, label: "Reason" },
    { id: "f_approver", type: FieldType.PRINCIPAL, label: "Approver" },
    { id: "f_due_approval", type: FieldType.DATETIME, label: "Approval due" },
  ],
  states: [
    { id: "pending", category: StateCategory.WAITING, label: "Waiting for approval", due: "f_due_approval" },
    { id: "approved", category: StateCategory.DONE, label: "Approved" },
    { id: "rejected", category: StateCategory.DONE, label: "Rejected" },
  ],
  transitions: [
    { fromState: "pending", toState: "rejected", allowed: ["field:f_approver"], label: "Reject" },
    { fromState: "pending", toState: "approved", allowed: ["field:f_approver"], label: "Approve", primary: true },
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
    { id: "fulfilled", category: StateCategory.DONE, label: "Fulfilled" },
    { id: "withdrawn", category: StateCategory.CANCELED, label: "Withdrawn" },
  ],
  transitions: [
    { fromState: "queue", toState: "in_hand", allowed: ["role:desk_agent"], except: ["creator"], label: "Take it", primary: true },
    { fromState: "in_hand", toState: "fulfilled", allowed: ["role:desk_agent"], label: "Mark fulfilled", primary: true },
    { fromState: "in_hand", toState: "queue", allowed: ["role:desk_agent"], label: "Hand back" },
    { fromState: "queue", toState: "withdrawn", allowed: ["creator"], label: "Withdraw" },
  ],
});

// No hints at all: the page falls back to IDs.
const bare = create(TypeSchema, {
  name: typeName("bare", 1),
  typeId: "bare",
  version: 1,
  fields: [{ id: "f_title", type: FieldType.STRING }],
  states: [
    { id: "open", category: StateCategory.WAITING },
    { id: "done", category: StateCategory.DONE },
    { id: "gone", category: StateCategory.CANCELED },
  ],
  transitions: [
    { fromState: "open", toState: "done", allowed: ["role:desk_agent"] },
    { fromState: "open", toState: "gone", allowed: ["role:desk_agent"] },
  ],
});

const people: DevUser[] = [
  { name: "ana", token: "a", principal: "user:ana", namespace: "acme", roles: [] },
  { name: "lea", token: "l", principal: "user:lea", namespace: "acme", roles: ["desk_agent"] },
  { name: "sam", token: "s", principal: "user:sam", namespace: "acme", roles: ["desk_agent"] },
];

const hoursAgo = (h: number) => timestampFromDate(new Date(Date.now() - h * 3_600_000 - 5_000));
const inHours = (h: number) => new Date(Date.now() + h * 3_600_000 + 60_000).toISOString();

function accessRow(n: number, title: string, dueInHours: number, moves = ["rejected", "approved"]): Item {
  return item({
    name: itemName(n),
    type: access.name,
    state: "pending",
    category: StateCategory.WAITING,
    creator: "user:ana",
    fields: { f_title: title, f_reason: "quarterly close", f_approver: "user:sam", f_due_approval: inHours(dueInHours) },
    stateTime: hoursAgo(3),
    moves,
  });
}

function queued(n: number, title: string, by: string, moves: string[], withheld: { toState: string; reason: WithheldReason }[] = []): Item {
  return item({
    name: itemName(n),
    type: equipment.name,
    state: "queue",
    category: StateCategory.WAITING,
    creator: by,
    fields: { f_title: title },
    stateTime: hoursAgo(n),
    moves,
    withheld,
  });
}

function handled(n: number, title: string, holder: string, moves: string[], withheld: { toState: string; reason: WithheldReason }[] = []): Item {
  return item({
    name: itemName(n),
    type: equipment.name,
    state: "in_hand",
    category: StateCategory.ACTIVE,
    creator: "user:ana",
    fields: { f_title: title, f_handler: holder },
    stateTime: hoursAgo(n),
    moves,
    withheld,
  });
}

type SearchReq = MessageInitShape<typeof SearchItemsRequestSchema>;
type CountReq = MessageInitShape<typeof CountItemsRequestSchema>;
type MoveReq = MessageInitShape<typeof TransitionItemRequestSchema>;
type Page = { items: Item[]; nextPageToken?: string };

// A fake kernel with only the four calls the Inbox may make.
function kernel(opts: {
  roles?: string[];
  rows: (req: SearchReq) => Page;
  count?: (req: CountReq) => number;
  move?: (req: MoveReq) => void;
}) {
  return {
    whoAmI: vi.fn(async () => create(WhoAmIResponseSchema, { principal: "user:sam", namespace: NS, roles: opts.roles ?? [] })),
    countItems: vi.fn(async (req: CountReq) => create(CountItemsResponseSchema, { count: opts.count ? opts.count(req) : 0 })),
    searchItems: vi.fn(async (req: SearchReq) => create(SearchItemsResponseSchema, opts.rows(req))),
    transitionItem: vi.fn(async (req: MoveReq) => {
      opts.move?.(req);
      return create(TransitionItemResponseSchema, { outcome: TransitionOutcome.APPLIED, commitSeq: 9n });
    }),
  };
}

function show(k: ReturnType<typeof kernel>, withPeople = true) {
  const user = { name: "sam", token: "s", principal: "user:sam", namespace: "acme", roles: ["desk_agent"] };
  const session = new Session(user, k as unknown as Api, [access, equipment, bare], ["user:sam", "user:lea"], withPeople ? people : []);
  return render(
    <SessionProvider value={session}>
      <InboxPage />
    </SessionProvider>,
  );
}

const inbox = () => screen.getByRole("region", { name: "Inbox" });
const list = (name: string) => within(inbox()).getByRole("list", { name });
const titlesIn = (name: string) => [...list(name).querySelectorAll<HTMLAnchorElement>(":scope > li > a")].map((a) => a.textContent);
const rowOf = (title: string) => screen.getByRole("link", { name: title }).closest("li")!;
const buttonsOf = (title: string) =>
  within(rowOf(title))
    .queryAllByRole("button")
    .map((b) => b.textContent);
const isTeam = (req: SearchReq) => req.filter?.waitingOn === "role:desk_agent";

test("a row: title, summary, Type and state, who asked, why it is yours, how long, and when it is due", async () => {
  const k = kernel({ rows: (req) => ({ items: isTeam(req) ? [] : [accessRow(1, "Finance share", 5)] }) });
  show(k);
  await waitFor(() => expect(titlesIn("Waiting")).toEqual(["Finance share"]));
  const row = rowOf("Finance share");
  expect(within(row).getByText("Reason: quarterly close")).toBeInTheDocument();
  expect(row).toHaveTextContent("access · Waiting for approval");
  expect(row).toHaveTextContent("Ana asks · you are its Approver · waiting 3 h · due in 5 h");
  // Not overdue: the due is said, not marked.
  expect(within(row).getByText("due in 5 h")).not.toHaveClass("overdue");
  // No IDs where the Type gives labels.
  expect(row).not.toHaveTextContent(/f_reason|f_approver|pending|access@2/);
});

test("overdue: past its due, the row is in Overdue, first, its due in amber with a word", async () => {
  const k = kernel({
    rows: (req) => ({ items: isTeam(req) ? [] : [accessRow(1, "On time", 5), accessRow(2, "Late", -4), accessRow(3, "Also on time", 9)] }),
  });
  show(k);
  await waitFor(() => expect(titlesIn("Overdue")).toEqual(["Late"]));
  expect(titlesIn("Waiting")).toEqual(["On time", "Also on time"]);
  // The headings, in that order.
  expect(within(inbox()).getAllByRole("heading", { level: 2 }).map((h) => h.textContent)).toEqual(["Overdue", "Waiting"]);
  const due = within(rowOf("Late")).getByText(/^due 4 h ago$/);
  expect(due).toHaveClass("overdue");
});

test("nothing overdue: no Overdue heading, only Waiting", async () => {
  const k = kernel({ rows: (req) => ({ items: isTeam(req) ? [] : [accessRow(1, "On time", 5)] }) });
  show(k);
  await waitFor(() => expect(titlesIn("Waiting")).toEqual(["On time"]));
  expect(within(inbox()).queryByRole("list", { name: "Overdue" })).not.toBeInTheDocument();
  expect(within(inbox()).getAllByRole("heading", { level: 2 }).map((h) => h.textContent)).toEqual(["Waiting"]);
});

test("the main button is the primary move when listed, the rest behind More moves (aria-expanded)", async () => {
  const k = kernel({ rows: (req) => ({ items: isTeam(req) ? [] : [accessRow(1, "Finance share", 5)] }) });
  show(k);
  await waitFor(() => expect(titlesIn("Waiting")).toEqual(["Finance share"]));
  // Approve is primary, though Reject comes first in the server's list.
  expect(buttonsOf("Finance share")).toEqual(["Approve", "More moves"]);
  const more = within(rowOf("Finance share")).getByRole("button", { name: "More moves" });
  expect(more).toHaveAttribute("aria-expanded", "false");
  await userEvent.click(more);
  expect(more).toHaveAttribute("aria-expanded", "true");
  const panel = document.getElementById(more.getAttribute("aria-controls")!)!;
  expect(panel).toBeInTheDocument();
  expect(within(panel).getAllByRole("button").map((b) => b.textContent)).toEqual(["Reject"]);
  await userEvent.click(more);
  expect(more).toHaveAttribute("aria-expanded", "false");
  expect(buttonsOf("Finance share")).toEqual(["Approve", "More moves"]);
});

test("primary not listed for the caller: the first listed move is the main button; one move: no More moves", async () => {
  const k = kernel({
    rows: (req) => ({ items: isTeam(req) ? [] : [accessRow(1, "Only reject", 5, ["rejected"]), accessRow(2, "Both", 5, ["rejected", "approved"])] }),
  });
  show(k);
  await waitFor(() => expect(titlesIn("Waiting")).toEqual(["Only reject", "Both"]));
  expect(buttonsOf("Only reject")).toEqual(["Reject"]);
  expect(buttonsOf("Both")).toEqual(["Approve", "More moves"]);
});

test("a Type with no hints: IDs, and Move to {state}", async () => {
  const bareRow = item({
    name: itemName(5),
    type: bare.name,
    state: "open",
    creator: "user:zoe",
    fields: { f_title: "Bare one" },
    stateTime: hoursAgo(1),
    moves: ["done", "gone"],
  });
  const k = kernel({ rows: (req) => ({ items: isTeam(req) ? [] : [bareRow] }) });
  show(k);
  await waitFor(() => expect(titlesIn("Waiting")).toEqual(["Bare one"]));
  expect(rowOf("Bare one")).toHaveTextContent("bare · open");
  // zoe is not in the dev-token file: her ID.
  expect(rowOf("Bare one")).toHaveTextContent("user:zoe asks · waiting 1 h");
  expect(buttonsOf("Bare one")).toEqual(["Move to done", "More moves"]);
});

test("names: from the dev-token file; without it, the ID", async () => {
  const k = kernel({ rows: (req) => ({ items: isTeam(req) ? [] : [accessRow(1, "Finance share", 5)] }) });
  show(k, false);
  await waitFor(() => expect(titlesIn("Waiting")).toEqual(["Finance share"]));
  expect(rowOf("Finance share")).toHaveTextContent("user:ana asks");
});

test("a move's answer is said with the move's label", async () => {
  let moved = false;
  const k = kernel({
    rows: (req) => ({ items: isTeam(req) || moved ? [] : [accessRow(1, "Finance share", 5)] }),
    move: () => (moved = true),
  });
  show(k);
  await waitFor(() => expect(titlesIn("Waiting")).toEqual(["Finance share"]));
  await userEvent.click(within(rowOf("Finance share")).getByRole("button", { name: "Approve" }));
  await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Finance share: Approve: APPLIED"));
  expect(k.transitionItem.mock.calls[0]![0]).toMatchObject({ name: itemName(1), toState: "approved" });
});

test("a team tab: its readable name, the queue's number, Nobody has these yet, then Being handled", async () => {
  const k = kernel({
    roles: ["desk_agent"],
    rows: (req) => {
      if (!isTeam(req)) return { items: [] };
      return req.filter?.claimed
        ? { items: [handled(4, "Keyboard", "user:lea", ["queue"], [{ toState: "fulfilled", reason: WithheldReason.CLAIMED_BY_OTHER }])] }
        : {
            items: [
              queued(9, "Second monitor", "user:ana", ["in_hand"]),
              queued(3, "Docking station", "user:sam", ["withdrawn"], [{ toState: "in_hand", reason: WithheldReason.OWN_REQUEST }]),
            ],
          };
    },
    count: (req) => (req.filter?.waitingOn === "role:desk_agent" ? 2 : 0),
  });
  show(k);
  const team = await screen.findByRole("tab", { name: /Desk agent/ });
  await waitFor(() => expect(team).toHaveTextContent("Desk agent 2"));
  await userEvent.click(team);
  await waitFor(() => expect(titlesIn("Nobody has these yet")).toEqual(["Second monitor", "Docking station"]));
  expect(titlesIn("Being handled")).toEqual(["Keyboard"]);
  expect(within(inbox()).getAllByRole("heading", { level: 2 }).map((h) => h.textContent)).toEqual(["Nobody has these yet", "Being handled"]);

  // Two searches, the queue and its claimed rows; one count, the queue's.
  const teamSearches = k.searchItems.mock.calls.map(([r]) => r).filter(isTeam);
  expect(teamSearches).toContainEqual({ parent: NS, pageSize: 50, orderBy: "waiting_since", filter: { waitingOn: "role:desk_agent" } });
  expect(teamSearches).toContainEqual({ parent: NS, pageSize: 50, orderBy: "waiting_since", filter: { waitingOn: "role:desk_agent", claimed: true } });
  for (const [req] of k.countItems.mock.calls) expect(req.filter?.claimed).toBeFalsy();

  // Take it, the primary; sam's own request says why he cannot take it,
  // and offers what is his: withdraw it.
  expect(buttonsOf("Second monitor")).toEqual(["Take it"]);
  expect(rowOf("Docking station")).toHaveTextContent("You asked for this one");
  expect(rowOf("Docking station")).toHaveTextContent("You can't handle your own request");
  expect(buttonsOf("Docking station")).toEqual(["Withdraw"]);
  // A claimed row: who has it, how long, and the colleague's one move.
  expect(rowOf("Keyboard")).toHaveTextContent("Lea has it");
  expect(rowOf("Keyboard")).toHaveTextContent("in hand 4 h");
  expect(buttonsOf("Keyboard")).toEqual(["Hand back"]);
  // "Lea has it" once, though it is also why the decision is withheld.
  expect(within(rowOf("Keyboard")).getAllByText("Lea has it")).toHaveLength(1);
});

test("a team tab's two sections page on their own: More, and More being handled", async () => {
  const queuePage1 = Array.from({ length: 50 }, (_, i) => queued(200 - i, `Queued ${i + 1}`, "user:ana", ["in_hand"]));
  const claimedPage1 = Array.from({ length: 50 }, (_, i) => handled(100 - i, `Handled ${i + 1}`, "user:lea", ["queue"]));
  const k = kernel({
    roles: ["desk_agent"],
    rows: (req) => {
      if (!isTeam(req)) return { items: [] };
      if (req.filter?.claimed) {
        return req.pageToken === "c2" ? { items: [handled(1, "Handled 51", "user:lea", ["queue"])] } : { items: claimedPage1, nextPageToken: "c2" };
      }
      return req.pageToken === "q2" ? { items: [queued(1, "Queued 51", "user:ana", ["in_hand"])] } : { items: queuePage1, nextPageToken: "q2" };
    },
  });
  show(k);
  await userEvent.click(await screen.findByRole("tab", { name: /Desk agent/ }));
  await waitFor(() => expect(titlesIn("Being handled")).toHaveLength(50));
  expect(titlesIn("Nobody has these yet")).toHaveLength(50);

  await userEvent.click(screen.getByRole("button", { name: "More being handled" }));
  await waitFor(() => expect(titlesIn("Being handled")).toHaveLength(51));
  expect(titlesIn("Nobody has these yet")).toHaveLength(50);
  expect(k.searchItems).toHaveBeenLastCalledWith({
    parent: NS,
    pageSize: 50,
    orderBy: "waiting_since",
    filter: { waitingOn: "role:desk_agent", claimed: true },
    pageToken: "c2",
  });
  expect(screen.queryByRole("button", { name: "More being handled" })).not.toBeInTheDocument();

  await userEvent.click(screen.getByRole("button", { name: "More" }));
  await waitFor(() => expect(titlesIn("Nobody has these yet")).toHaveLength(51));
  expect(k.searchItems).toHaveBeenLastCalledWith({
    parent: NS,
    pageSize: 50,
    orderBy: "waiting_since",
    filter: { waitingOn: "role:desk_agent" },
    pageToken: "q2",
  });
});

test("a team tab with nothing: the empty sentence names the team readably", async () => {
  const k = kernel({ roles: ["desk_agent"], rows: () => ({ items: [] }) });
  show(k);
  await userEvent.click(await screen.findByRole("tab", { name: /Desk agent/ }));
  expect(await screen.findByText("Nothing is waiting on Desk agent.")).toBeInTheDocument();
});

test("accessible as built: the sections are labelled lists, and Tab goes title, main move, More moves, row by row", async () => {
  const k = kernel({ rows: (req) => ({ items: isTeam(req) ? [] : [accessRow(1, "First", 5), accessRow(2, "Second", 6)] }) });
  show(k);
  await waitFor(() => expect(titlesIn("Waiting")).toEqual(["First", "Second"]));
  expect(inbox().tagName).toBe("SECTION");
  screen.getByRole("link", { name: "First" }).focus();
  const order = [document.activeElement?.textContent];
  for (let i = 0; i < 5; i++) {
    await userEvent.tab();
    order.push(document.activeElement?.textContent);
  }
  expect(order).toEqual(["First", "Approve", "More moves", "Second", "Approve", "More moves"]);
});

// The amber of an overdue due: at least 4.5:1 against the page and a
// row's white (WCAG AA for text), read from the stylesheet itself.
test("the overdue amber's contrast is at least 4.5:1", () => {
  // vitest runs in web/.
  const css = readFileSync(resolve(process.cwd(), "src/styles.css"), "utf8");
  const rule = /\.overdue\s*\{[^}]*?[^-]color:\s*(#[0-9a-fA-F]{6})/.exec(css);
  expect(rule, "a .overdue rule with a #rrggbb color in styles.css").not.toBeNull();
  const lum = (hex: string) => {
    const c = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255).map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
    return 0.2126 * c[0]! + 0.7152 * c[1]! + 0.0722 * c[2]!;
  };
  const ratio = (a: string, b: string) => {
    const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p);
    return (x! + 0.05) / (y! + 0.05);
  };
  for (const ground of ["#ffffff", "#fafafa"]) expect(ratio(rule![1]!, ground)).toBeGreaterThanOrEqual(4.5);
});
