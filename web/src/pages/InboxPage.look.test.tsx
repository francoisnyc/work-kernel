// The Inbox, the look (docs/plans/inbox-the-look.md, and the owner's
// answers at Phase A, 2026-11-28), on the screen against a fake kernel.
// How it reads and how it is reached; what the page does is unchanged.
//
//   - a Type by its label beside the title ("Access request · Waiting for
//     approval"), the newest version's label when the Item's has none;
//   - a row with nothing for the reader says why, with no link (its title
//     opens it); a row sent back to the reader keeps "Open it to answer";
//   - an instant in words, the full instant in a tooltip, never raw;
//   - an agent's text once: the panel's field list leaves out what is in
//     "What the agent wrote";
//   - an agent by its display name from the dev-token file; its badge
//     carries a robot icon beside the words, never colour alone;
//   - the other moves behind a "⋯" disclosure button labelled "More
//     actions": aria-expanded, real buttons inside, Escape closes it and
//     puts focus back on it;
//   - contrast 4.5:1, touch targets 44 px, focus visible.
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { Session, SessionProvider, type Api } from "../app/session";
import {
  CountItemsResponseSchema,
  FieldType,
  ItemEventKind,
  ListItemEventsResponseSchema,
  SearchItemsResponseSchema,
  StateCategory,
  TypeSchema,
  WhoAmIResponseSchema,
  WithheldReason,
  type Item,
  type ListItemEventsRequestSchema,
  type SearchItemsRequestSchema,
  type Type,
} from "../gen/kernel/v1/kernel_pb";
import type { DevAgent, DevUser } from "../model/devusers";
import { NS, item, itemName, typeName } from "../test/fixtures";
import { InboxPage } from "./InboxPage";

// access@1 has no label of its own; access@2 does. The same states.
const accessOf = (version: number, label: string): Type =>
  create(TypeSchema, {
    name: typeName("access", version),
    typeId: "access",
    version,
    label,
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
const access1 = accessOf(1, "");
const access2 = accessOf(2, "Access request");

// A desk request with a claim, no label on any version.
const desk = create(TypeSchema, {
  name: typeName("desk_request", 1),
  typeId: "desk_request",
  version: 1,
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

// A ticket an agent works on.
const ticket = create(TypeSchema, {
  name: typeName("ticket", 9),
  typeId: "ticket",
  version: 9,
  fields: [
    { id: "f_title", type: FieldType.STRING, label: "Title" },
    { id: "f_priority", type: FieldType.INT, label: "Priority" },
    { id: "f_note", type: FieldType.STRING, label: "Note" },
  ],
  states: [
    { id: "in_progress", category: StateCategory.ACTIVE, label: "In progress" },
    { id: "approved", category: StateCategory.ACTIVE, label: "Approved" },
  ],
  transitions: [{ fromState: "in_progress", toState: "approved", allowed: ["role:approver"], label: "Approve", primary: true }],
});

const people: DevUser[] = [
  { name: "ana", token: "a", principal: "user:ana", namespace: "acme", roles: [] },
  { name: "lea", token: "l", principal: "user:lea", namespace: "acme", roles: ["desk_agent"] },
  { name: "sam", token: "s", principal: "user:sam", namespace: "acme", roles: ["desk_agent"] },
];
const agents: DevAgent[] = [{ name: "triage-bot", principal: "agent:triage-bot", namespace: "acme", displayName: "Triage agent" }];

const HOUR = 3_600_000;
const ago = (ms: number) => timestampFromDate(new Date(Date.now() - ms));
// A due instant some time ahead, a few seconds over so the words do not
// round down to the hour before while the test runs.
const ahead = (ms: number) => new Date(Date.now() + ms + 30_000).toISOString();

function accessRow(n: number, title: string, type: Type, moves = ["rejected", "approved"]): Item {
  return item({
    name: itemName(n),
    type: type.name,
    state: "pending",
    category: StateCategory.WAITING,
    creator: "user:ana",
    fields: { f_title: title, f_reason: `reason ${n}`, f_approver: "user:sam", f_due_approval: ahead(5 * HOUR) },
    stateTime: ago(3 * HOUR),
    moves,
  });
}

const RAW = "The parcel arrived damaged.";
const agentsRow = item({
  name: itemName(20),
  type: ticket.name,
  state: "in_progress",
  category: StateCategory.ACTIVE,
  creator: "user:ana",
  stateActor: "agent:triage-bot",
  stateActorOnBehalfOf: "user:ana",
  fields: { f_title: "Printer jams", f_priority: 2, f_note: RAW },
  stateTime: ago(60_000),
  moves: ["approved"],
});
const historyOf20 = [
  { name: `${itemName(20)}/events/1`, itemSeq: 1n, kind: ItemEventKind.CREATED, actor: "user:ana", fields: { f_title: "Printer jams" } },
  { name: `${itemName(20)}/events/2`, itemSeq: 2n, kind: ItemEventKind.UPDATED, actor: "agent:triage-bot", onBehalfOf: "user:ana", fields: { f_priority: 2, f_note: RAW } },
];

type SearchReq = MessageInitShape<typeof SearchItemsRequestSchema>;
type EventsReq = MessageInitShape<typeof ListItemEventsRequestSchema>;

function kernel(rows: Item[], roles: string[] = []) {
  return {
    whoAmI: vi.fn(async () => create(WhoAmIResponseSchema, { principal: "user:sam", namespace: NS, roles })),
    countItems: vi.fn(async () => create(CountItemsResponseSchema, { count: rows.length })),
    searchItems: vi.fn(async (req: SearchReq) => create(SearchItemsResponseSchema, { items: req.filter?.waitingOn === "me" ? rows : [] })),
    transitionItem: vi.fn(),
    listItemEvents: vi.fn(async (req: EventsReq) =>
      create(ListItemEventsResponseSchema, { events: req.parent === itemName(20) ? (historyOf20 as never) : [] }),
    ),
  };
}

function show(k: ReturnType<typeof kernel>, types: Type[] = [access1, access2, desk, ticket]) {
  const user = { name: "sam", token: "s", principal: "user:sam", namespace: "acme", roles: [] };
  const session = new Session(user, k as unknown as Api, types, ["user:sam", "user:lea"], people, agents);
  return render(
    <SessionProvider value={session}>
      <InboxPage />
    </SessionProvider>,
  );
}

const inbox = () => screen.getByRole("region", { name: "Inbox" });
const rowOf = (title: string) => within(inbox()).getByRole("link", { name: title }).closest("li")!;

beforeEach(() => {
  window.location.hash = "#/inbox";
});
afterEach(() => {
  window.location.hash = "";
});

test("a Type by its label: its own, else the newest version's; the ID when none has one", async () => {
  const held = item({
    name: itemName(3),
    type: desk.name,
    state: "in_hand",
    creator: "user:ana",
    fields: { f_title: "Keyboard", f_handler: "user:lea" },
    stateTime: ago(HOUR),
    moves: [],
    withheld: [{ toState: "queue", reason: WithheldReason.CLAIMED_BY_OTHER }],
  });
  show(kernel([accessRow(1, "Old share", access1), accessRow(2, "New share", access2), held]));
  await screen.findByRole("link", { name: "Old share" });
  expect(rowOf("Old share")).toHaveTextContent("Access request · Waiting for approval");
  expect(rowOf("New share")).toHaveTextContent("Access request · Waiting for approval");
  expect(rowOf("Old share")).not.toHaveTextContent(/\baccess ·/);
  expect(rowOf("Keyboard")).toHaveTextContent("desk_request · Being handled");
  // The panel says it the same way.
  await userEvent.click(screen.getByRole("link", { name: "Old share" }));
  const aside = await screen.findByRole("complementary", { name: "Old share" });
  expect(aside).toHaveTextContent("Access request · Waiting for approval");
});

test("why it is yours: {label}: you", async () => {
  show(kernel([accessRow(1, "Finance share", access2)]));
  await screen.findByRole("link", { name: "Finance share" });
  expect(rowOf("Finance share")).toHaveTextContent("Ana asks · Approver: you · waiting 3 h");
  expect(rowOf("Finance share")).not.toHaveTextContent("you are its");
});

test("a row with nothing for the reader says why and offers no link; its title still opens it", async () => {
  const held = item({
    name: itemName(3),
    type: desk.name,
    state: "in_hand",
    creator: "user:ana",
    fields: { f_title: "Keyboard", f_handler: "user:lea" },
    stateTime: ago(HOUR),
    moves: [],
    withheld: [{ toState: "queue", reason: WithheldReason.CLAIMED_BY_OTHER }],
  });
  const own = item({
    name: itemName(4),
    type: desk.name,
    state: "queue",
    creator: "user:sam",
    fields: { f_title: "Docking station" },
    stateTime: ago(HOUR),
    moves: [],
    withheld: [{ toState: "in_hand", reason: WithheldReason.OWN_REQUEST }],
  });
  show(kernel([held, own]));
  await screen.findByRole("link", { name: "Keyboard" });
  expect(rowOf("Keyboard")).toHaveTextContent("Lea has it");
  expect(rowOf("Docking station")).toHaveTextContent("You can't handle your own request");
  for (const title of ["Keyboard", "Docking station"]) {
    expect(within(rowOf(title)).getAllByRole("link").map((a) => a.textContent)).toEqual([title]);
    expect(within(rowOf(title)).queryAllByRole("button")).toEqual([]);
  }
  await userEvent.click(screen.getByRole("link", { name: "Keyboard" }));
  expect(await screen.findByRole("complementary", { name: "Keyboard" })).toBeInTheDocument();
});

test("an instant in words, never raw: the panel's datetime field and the row's due, each with the full instant as a tooltip", async () => {
  const row = accessRow(1, "Finance share", access2);
  const raw = row.fields!.f_due_approval as string;
  show(kernel([row]));
  await screen.findByRole("link", { name: "Finance share" });
  const due = rowOf("Finance share").querySelector(".due")!;
  expect(due).toHaveTextContent("due in 5 h");
  expect(due.getAttribute("title")).toContain(String(new Date(raw).getFullYear()));

  await userEvent.click(screen.getByRole("link", { name: "Finance share" }));
  const aside = await screen.findByRole("complementary", { name: "Finance share" });
  const fields = within(aside).getByRole("list", { name: "Fields" });
  expect(fields).toHaveTextContent("Approval due");
  expect(fields).toHaveTextContent("in 5 h");
  expect(fields).not.toHaveTextContent(raw);
  expect(fields).not.toHaveTextContent(/\d{4}-\d{2}-\d{2}T/);
  const when = within(fields).getByTitle(/\d{4}/);
  expect(when.getAttribute("title")).toContain(String(new Date(raw).getFullYear()));
});

test("an agent's request: its name from the dev-token file, its badge with a robot icon, its text once", async () => {
  show(kernel([agentsRow]));
  await screen.findByRole("link", { name: "Printer jams" });
  const badge = rowOf("Printer jams").querySelector(".agent-badge")!;
  expect(badge).toHaveTextContent("Agent request · for Ana");
  const icon = badge.querySelector("svg");
  expect(icon).not.toBeNull();
  expect(icon!.getAttribute("aria-hidden")).toBe("true");

  await userEvent.click(screen.getByRole("link", { name: "Printer jams" }));
  const aside = await screen.findByRole("complementary", { name: "Printer jams" });
  expect(aside).toHaveTextContent("Triage agent, for Ana");
  expect(aside).not.toHaveTextContent("triage-bot");
  const wrote = await within(aside).findByRole("region", { name: "What the agent wrote" });
  expect(wrote).toHaveTextContent("Priority");
  expect(wrote).toHaveTextContent(RAW);
  // Once: the field list leaves out what the box shows.
  const fields = within(aside).getByRole("list", { name: "Fields" });
  expect(fields).toHaveTextContent("Title");
  expect(fields).not.toHaveTextContent("Priority");
  expect(fields).not.toHaveTextContent("Note");
  expect(aside.textContent!.split(RAW)).toHaveLength(2);
});

test("an agent's text is in the field list while the box is not shown (history not read yet)", async () => {
  const k = kernel([agentsRow]);
  k.listItemEvents.mockImplementation(async () => create(ListItemEventsResponseSchema, { events: [] }));
  show(k);
  await userEvent.click(await screen.findByRole("link", { name: "Printer jams" }));
  const aside = await screen.findByRole("complementary", { name: "Printer jams" });
  await waitFor(() => expect(k.listItemEvents).toHaveBeenCalled());
  expect(within(aside).queryByRole("region", { name: "What the agent wrote" })).not.toBeInTheDocument();
  expect(within(aside).getByRole("list", { name: "Fields" })).toHaveTextContent("Priority");
});

test("More actions: a ⋯ disclosure button; Enter opens it, its moves are real buttons, Escape closes it and returns focus", async () => {
  show(kernel([accessRow(1, "Finance share", access2)]));
  await screen.findByRole("link", { name: "Finance share" });
  const more = within(rowOf("Finance share")).getByRole("button", { name: "More actions" });
  expect(more).toHaveTextContent("⋯");
  expect(more).toHaveAttribute("aria-expanded", "false");
  const disclosed = document.getElementById(more.getAttribute("aria-controls")!)!;
  expect(disclosed).not.toBeVisible();

  // By keyboard: from the main move, Tab reaches it; Enter opens it.
  within(rowOf("Finance share")).getByRole("button", { name: "Approve" }).focus();
  await userEvent.tab();
  expect(more).toHaveFocus();
  await userEvent.keyboard("{Enter}");
  expect(more).toHaveAttribute("aria-expanded", "true");
  expect(disclosed).toBeVisible();
  expect(within(disclosed).getAllByRole("button").map((b) => b.textContent)).toEqual(["Reject"]);

  // Tab goes into it; Escape from inside closes it and puts focus back.
  await userEvent.tab();
  expect(within(disclosed).getByRole("button", { name: "Reject" })).toHaveFocus();
  await userEvent.keyboard("{Escape}");
  expect(more).toHaveAttribute("aria-expanded", "false");
  expect(disclosed).not.toBeVisible();
  expect(more).toHaveFocus();

  // Space opens it again; Escape on the button itself closes it.
  await userEvent.keyboard(" ");
  expect(more).toHaveAttribute("aria-expanded", "true");
  await userEvent.keyboard("{Escape}");
  expect(more).toHaveAttribute("aria-expanded", "false");
  expect(more).toHaveFocus();
  // Nothing was moved.
  expect(screen.queryByRole("status")).not.toBeInTheDocument();
});

// The stylesheet itself: the agent badge's purple on its tint at 4.5:1 or
// more, the main move's white on its dark, a visible focus ring, and 44 px
// touch targets.
test("styles: contrast 4.5:1 for the badge and the main move, focus visible, 44 px targets", () => {
  const css = readFileSync(resolve(process.cwd(), "src/styles.css"), "utf8");
  const lum = (hex: string) => {
    const c = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255).map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
    return 0.2126 * c[0]! + 0.7152 * c[1]! + 0.0722 * c[2]!;
  };
  const ratio = (a: string, b: string) => {
    const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p);
    return (x! + 0.05) / (y! + 0.05);
  };
  const colours = (selector: string) => {
    const body = new RegExp(`${selector}\\s*\\{([^}]*)\\}`).exec(css)?.[1] ?? "";
    return {
      fg: /(?:^|[;\s])color:\s*(#[0-9a-fA-F]{6})/.exec(body)?.[1],
      bg: /background(?:-color)?:\s*(#[0-9a-fA-F]{6})/.exec(body)?.[1],
    };
  };
  const badge = colours("\\.agent-badge");
  expect(badge.fg && badge.bg, "an .agent-badge rule with a #rrggbb color and background").toBeTruthy();
  expect(ratio(badge.fg!, badge.bg!)).toBeGreaterThanOrEqual(4.5);
  const main = colours("\\.main-move");
  expect(main.fg && main.bg, "a .main-move rule with a #rrggbb color and background").toBeTruthy();
  expect(ratio(main.fg!, main.bg!)).toBeGreaterThanOrEqual(4.5);
  expect(css).toMatch(/:focus-visible\s*\{[^}]*outline:\s*\d/);
  expect(css).toMatch(/min-height:\s*44px/);
});

test("the main move is the row's .main-move button, the one dark button", async () => {
  show(kernel([accessRow(1, "Finance share", access2)]));
  await screen.findByRole("link", { name: "Finance share" });
  const main = within(rowOf("Finance share")).getByRole("button", { name: "Approve" });
  expect(main).toHaveClass("main-move");
  const more = within(rowOf("Finance share")).getByRole("button", { name: "More actions" });
  expect(more).not.toHaveClass("main-move");
});

test("the panel's moves: in one row, the main move first and dark", async () => {
  show(kernel([accessRow(1, "Finance share", access2)]));
  await userEvent.click(await screen.findByRole("link", { name: "Finance share" }));
  const aside = await screen.findByRole("complementary", { name: "Finance share" });
  const moves = within(aside).getByRole("group", { name: "Moves" });
  expect(within(moves).getAllByRole("button").map((b) => b.textContent)).toEqual(["Approve", "Reject"]);
  expect(within(moves).getByRole("button", { name: "Approve" })).toHaveClass("main-move");
  expect(within(moves).getByRole("button", { name: "Reject" })).not.toHaveClass("main-move");
});
