// The Inbox for people, PR 3b: an agent's request on the screen, against a
// fake kernel. The owner's decisions (PR 3's Phase A, 2026-11-27):
//
//   - a row an agent created or put in its state reads "Agent request ·
//     for {person}" ("for you" for the reader);
//   - its panel says "{agent}, for {person}" and, from the history read
//     when the panel opens on such a row, "What the agent wrote": the
//     fields an agent set last, as plain text (pre-wrap; no markdown,
//     links or HTML), under the warning "Written by an agent. Check it
//     before you decide.";
//   - any other row's panel reads its history only when unfolded, as in 2c.
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
  type Item,
  type ListItemEventsRequestSchema,
  type SearchItemsRequestSchema,
} from "../gen/kernel/v1/kernel_pb";
import type { DevUser } from "../model/devusers";
import { NS, item, itemName, typeName } from "../test/fixtures";
import { InboxPage } from "./InboxPage";

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
  { name: "bob", token: "b", principal: "user:bob", namespace: "acme", roles: ["approver"] },
  { name: "kim", token: "k", principal: "customer:kim", namespace: "acme", roles: [] },
];

const RAW = "<b>x</b> [a](javascript:alert(1)) **bold**\nsecond line";

function ticketRow(n: number, title: string, by: { stateActor: string; stateActorOnBehalfOf?: string; creator?: string; creatorOnBehalfOf?: string }): Item {
  return item({
    name: itemName(n),
    type: ticket.name,
    state: "in_progress",
    category: StateCategory.ACTIVE,
    creator: by.creator ?? "user:ana",
    creatorOnBehalfOf: by.creatorOnBehalfOf ?? "",
    stateActor: by.stateActor,
    stateActorOnBehalfOf: by.stateActorOnBehalfOf ?? "",
    fields: { f_title: title, f_priority: 2, f_note: RAW },
    stateTime: timestampFromDate(new Date(Date.now() - 60_000)),
    moves: ["approved"],
  });
}

const agentsRow = ticketRow(1, "Printer jams", { stateActor: "agent:triage-bot", stateActorOnBehalfOf: "user:ana" });
const peoplesRow = ticketRow(2, "Badge lost", { stateActor: "user:ana" });

// Ana created it; the agent, for her, set the priority and the note.
const historyOf1 = [
  { name: `${itemName(1)}/events/1`, itemSeq: 1n, kind: ItemEventKind.CREATED, actor: "user:ana", fields: { f_title: "Printer jams" } },
  {
    name: `${itemName(1)}/events/2`,
    itemSeq: 2n,
    kind: ItemEventKind.UPDATED,
    actor: "agent:triage-bot",
    onBehalfOf: "user:ana",
    fields: { f_priority: 2, f_note: RAW },
  },
];

type SearchReq = MessageInitShape<typeof SearchItemsRequestSchema>;
type EventsReq = MessageInitShape<typeof ListItemEventsRequestSchema>;

function kernel(rows: Item[], principal = "user:bob") {
  return {
    whoAmI: vi.fn(async () => create(WhoAmIResponseSchema, { principal, namespace: NS, roles: [] })),
    countItems: vi.fn(async () => create(CountItemsResponseSchema, { count: rows.length })),
    searchItems: vi.fn(async (_req: SearchReq) => create(SearchItemsResponseSchema, { items: rows })),
    transitionItem: vi.fn(),
    listItemEvents: vi.fn(async (req: EventsReq) =>
      create(ListItemEventsResponseSchema, { events: req.parent === itemName(1) ? (historyOf1 as never) : [] }),
    ),
  };
}

function show(k: ReturnType<typeof kernel>, principal = "user:bob") {
  const user = { name: principal.split(":")[1]!, token: "t", principal, namespace: "acme", roles: [] };
  const session = new Session(user, k as unknown as Api, [ticket], ["user:bob"], people);
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

const rowOf = (title: string) => within(screen.getByRole("region", { name: "Inbox" })).getByRole("link", { name: title }).closest("li")!;

test("a row an agent put in its state reads Agent request · for Ana; a person's row does not", async () => {
  show(kernel([agentsRow, peoplesRow]));
  await screen.findByRole("link", { name: "Printer jams" });
  expect(rowOf("Printer jams")).toHaveTextContent("Agent request · for Ana");
  expect(rowOf("Badge lost")).not.toHaveTextContent("Agent request");
});

test("a row an agent created for the reader: Agent request · for you", async () => {
  const kims = ticketRow(3, "Scanner offline", { creator: "agent:portal-bot", creatorOnBehalfOf: "customer:kim", stateActor: "user:sam" });
  show(kernel([kims], "customer:kim"), "customer:kim");
  await screen.findByRole("link", { name: "Scanner offline" });
  expect(rowOf("Scanner offline")).toHaveTextContent("Agent request · for you");
});

test("the panel of an agent's request: who, and What the agent wrote, from the history read on opening, under the warning", async () => {
  const k = kernel([agentsRow, peoplesRow]);
  show(k);
  await userEvent.click(await screen.findByRole("link", { name: "Printer jams" }));
  const aside = await screen.findByRole("complementary", { name: "Printer jams" });
  expect(aside).toHaveTextContent("triage-bot, for Ana");
  // Read on opening, not only on unfolding the history.
  await waitFor(() => expect(k.listItemEvents).toHaveBeenCalledWith(expect.objectContaining({ parent: itemName(1) })));
  const wrote = await within(aside).findByRole("region", { name: "What the agent wrote" });
  expect(wrote).toHaveTextContent("Written by an agent. Check it before you decide.");
  expect(wrote).toHaveTextContent("Priority");
  expect(wrote).toHaveTextContent("2");
  expect(wrote).toHaveTextContent("Note");
  // Ana's title is not the agent's.
  expect(wrote).not.toHaveTextContent("Printer jams");
});

test("what the agent wrote is plain text: no HTML, no link, no markdown, its line breaks kept", async () => {
  show(kernel([agentsRow]));
  await userEvent.click(await screen.findByRole("link", { name: "Printer jams" }));
  const wrote = await screen.findByRole("region", { name: "What the agent wrote" });
  const note = await within(wrote).findByText((_, el) => el?.classList.contains("agent-text") === true && el.textContent === RAW);
  expect(note.textContent).toBe(RAW);
  expect(wrote.querySelector("b, strong, em, a, script, img")).toBeNull();
  // Line breaks kept by the stylesheet, not by markup.
  const css = readFileSync(resolve(process.cwd(), "src/styles.css"), "utf8");
  expect(/\.agent-text\s*\{[^}]*white-space:\s*pre-wrap/.test(css)).toBe(true);
});

test("a person's row: no agent line, no section, and the history is read only when unfolded", async () => {
  const k = kernel([peoplesRow]);
  show(k);
  await userEvent.click(await screen.findByRole("link", { name: "Badge lost" }));
  const aside = await screen.findByRole("complementary", { name: "Badge lost" });
  expect(within(aside).queryByRole("region", { name: "What the agent wrote" })).not.toBeInTheDocument();
  expect(aside).not.toHaveTextContent(/triage-bot|Agent request/);
  await new Promise((r) => setTimeout(r, 30));
  expect(k.listItemEvents).not.toHaveBeenCalled();
});
