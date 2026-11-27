// The Inbox on the waiting-on view (track 1 item 5, step 4, PR 2), in the
// browser against the compose stack. The page asks the kernel what waits
// (SearchItems with waiting_on), how many (CountItems), who is asking
// (WhoAmI) and which moves a row offers (Item.moves); it decides nothing.
//
//   1. sam, a desk agent, works the desk agents' queue: the tab and its
//      number, oldest waiting first, a move made from the row.
//   2. ana, with nothing waiting below a place, sees the empty state.
//   3. bob, an approver with 51 waiting below a place, sees "50+" and
//      reads the next page with More.
//   4. kim, a customer: one tab, and the page asks for her own view only.
//   5. sam claims an equipment request from the desk agents' queue; lea, a
//      desk agent too, cannot decide it; sam releases it and it is back at
//      the end of the queue (equipment_request@1, the first Type in types/
//      with a claimed state).
//
// Journeys 1 to 3 narrow the Inbox to a place they have just made: the
// stack keeps what earlier runs and the scenario left waiting. Journey 5's
// requests are at the top level, so it reads newest first and finds its
// rows by their titles.
//   6. sam sends kim's incident back to her (incident@6: waiting for its
//      customer waits on its requester): it is in her Inbox, labelled "sent
//      back to you", with no button and a link; she answers on its page with
//      a reply, which brings it back to in_progress; it leaves her Inbox.
//
// Run: docker compose up -d --build --wait, then `npm run e2e` in web/.
import { expect, test, type Page } from "@playwright/test";
import {
  api,
  cancelAll,
  idOf,
  seedApprovalQueue,
  seedDeskQueue,
  seedEmptyPlace,
  seedEquipmentRequests,
  seedSentBack,
  withdrawAll,
} from "./seed";
import type { Item } from "../src/gen/kernel/v1/kernel_pb";

const run = Date.now().toString(36);

// What the journeys left waiting: canceled when they are done, so that
// the other journeys' Inboxes are as they found them.
const left: Item[] = [];
const asked: Item[] = []; // equipment requests: withdrawn, not canceled
test.afterAll(async ({ baseURL }) => {
  await cancelAll(baseURL!, left);
  await withdrawAll(baseURL!, asked);
});

async function openInbox(page: Page, name: string) {
  await page.goto("/");
  await page.getByLabel("Act as").selectOption(name);
  await expect(page.getByRole("banner")).toContainText(`acting as ${name}`);
  await page.getByRole("link", { name: "Inbox" }).click();
  await expect(page.getByRole("heading", { name: "Inbox" })).toBeVisible();
}

async function narrowTo(page: Page, placeId: string) {
  await page.getByLabel("Place").fill(placeId);
  await page.getByRole("button", { name: "Apply" }).click();
}

const inbox = (page: Page) => page.getByRole("list", { name: "Inbox" });
// The rows' titles, in the order shown.
const titles = (page: Page) => inbox(page).locator("> li > a");

test("sam works the desk agents' queue: its tab and number, oldest first, a move from the row", async ({ page, baseURL }) => {
  const names = [`VPN down ${run}`, `Printer jam ${run}`, `Badge lost ${run}`];
  const queue = await seedDeskQueue(baseURL!, run, names);
  left.push(...queue.incidents);

  await openInbox(page, "sam");
  // sam holds desk_agent and ticket_admin; the Types name both in a move.
  await expect(page.getByRole("tab", { name: /Waiting on me/ })).toHaveAttribute("aria-selected", "true");
  await page.getByRole("tab", { name: /desk_agent/ }).click();
  await narrowTo(page, idOf(queue.desk));

  // The tab's number is the kernel's count of that queue below the desk.
  await expect(page.getByRole("tab", { name: /desk_agent/ })).toHaveText(/desk_agent\s*3/);
  // Oldest waiting first: the order they were created in.
  await expect(titles(page)).toHaveText(names);
  const first = inbox(page).locator("> li").filter({ hasText: names[0]! });
  await expect(first).toContainText("incident@3 · new");
  await expect(first).toContainText("asked by user:ana");
  await expect(first).toContainText(/waiting \d+ (s|min)|just arrived/);
  await expect(first).toContainText("for your team (desk_agent)");
  // The buttons are the moves the server lists for sam, in its order.
  await expect(first.getByRole("button")).toHaveText(["Move to in_progress", "Move to canceled"]);

  await page.getByLabel("Order").selectOption("Newest waiting first");
  await expect(titles(page)).toHaveText([...names].reverse());
  await page.getByLabel("Order").selectOption("Oldest waiting first");
  await expect(titles(page)).toHaveText(names);

  // The move: one TransitionItem. In progress, the incident names no desk
  // agent any more: it leaves the queue, and the number follows.
  await first.getByRole("button", { name: "Move to in_progress" }).click();
  await expect(page.getByRole("status")).toContainText("APPLIED");
  await expect(titles(page)).toHaveText(names.slice(1));
  await expect(page.getByRole("tab", { name: /desk_agent/ })).toHaveText(/desk_agent\s*2/);
  await expect(page.getByRole("alert")).toHaveCount(0);

  const got = await api(baseURL!, "sam").getItem({ name: queue.incidents[0]!.name });
  expect(got.item?.state).toBe("in_progress");
});

test("ana, with nothing waiting below a place, sees the empty state", async ({ page, baseURL }) => {
  const place = await seedEmptyPlace(baseURL!, run);
  await openInbox(page, "ana");
  await narrowTo(page, idOf(place));
  await expect(page.getByText("Nothing is waiting on you.")).toBeVisible();
  await expect(page.getByRole("tab", { name: /Waiting on me/ })).toHaveText(/Waiting on me\s*0/);
  await expect(inbox(page).locator("> li")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "More" })).toHaveCount(0);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("bob has 51 approvals waiting below a place: 50+ on the tab, and More loads the last one", async ({ page, baseURL }) => {
  const queue = await seedApprovalQueue(baseURL!, run, 51);
  left.push(...queue.tickets);
  await openInbox(page, "bob");
  await narrowTo(page, idOf(queue.box));

  await expect(page.getByRole("tab", { name: /Waiting on me/ })).toHaveText(/Waiting on me\s*50\+/);
  await expect(titles(page)).toHaveCount(50);
  // Oldest waiting first: the first to reach in_progress leads.
  await expect(titles(page).first()).toHaveText(`Approve ${run} #1`);
  await expect(inbox(page)).not.toContainText(`Approve ${run} #51`);

  await page.getByRole("button", { name: "More" }).click();
  await expect(titles(page)).toHaveCount(51);
  await expect(titles(page).last()).toHaveText(`Approve ${run} #51`);
  await expect(titles(page).first()).toHaveText(`Approve ${run} #1`);
  await expect(page.getByRole("button", { name: "More" })).toHaveCount(0);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

// Since step 5 a customer's Inbox asks like anyone's: who she is, her one
// tab's number, and her own view. Never a role's queue: she holds none.
test("kim, a customer: one tab, and the page asks for her own view only", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Act as").selectOption("kim");
  await expect(page.getByRole("banner")).toContainText("acting as kim");
  // Only what the Inbox itself asks counts: the page before it has read.
  await page.waitForLoadState("networkidle");
  const calls: { rpc: string; body: string }[] = [];
  page.on("request", (req) => {
    if (req.url().includes("/api/")) calls.push({ rpc: req.url().split("/").pop()!, body: req.postData() ?? "" });
  });
  await page.getByRole("link", { name: "Inbox" }).click();
  await expect(page.getByRole("heading", { name: "Inbox" })).toBeVisible();
  await expect(page.getByRole("tab")).toHaveText([/^Waiting on me\s*\d+\+?$/]);
  await expect(page.getByText("Nothing waits on you here yet.")).toHaveCount(0);
  await page.waitForLoadState("networkidle");
  await expect(page.getByRole("alert")).toHaveCount(0);
  expect(new Set(calls.map((c) => c.rpc))).toEqual(new Set(["WhoAmI", "CountItems", "SearchItems"]));
  for (const c of calls.filter((c) => c.rpc !== "WhoAmI")) {
    expect(c.body).toContain('"waitingOn":"me"');
    expect(c.body).not.toContain("role:");
  }
});

test("sam claims an equipment request; lea cannot decide it; sam releases it to the end of the queue", async ({ page, baseURL }) => {
  // A word of this journey's own: the other journeys' rows carry `run`.
  const word = `eq${run}`;
  const laptop = `Laptop ${word}`;
  const monitor = `Monitor ${word}`;
  const made = await seedEquipmentRequests(baseURL!, [laptop, monitor]);
  asked.push(...made);
  const first = made[0];
  // This run's rows, in the order shown. Newest waiting first, so they
  // lead the list whatever earlier runs left in the queue.
  const mine = (p: Page) => titles(p).filter({ hasText: word });
  const row = (p: Page, title: string) => inbox(p).locator("> li").filter({ hasText: title });

  await openInbox(page, "sam");
  await page.getByLabel("Order").selectOption("Newest waiting first");
  await page.getByRole("tab", { name: /desk_agent/ }).click();
  // Both wait on the team: the monitor arrived last. The one move a desk
  // agent has in the queue is to take the request.
  await expect(mine(page)).toHaveText([monitor, laptop]);
  await expect(row(page, laptop)).toContainText("equipment_request@1 · queue");
  await expect(row(page, laptop)).toContainText("for your team (desk_agent)");
  await expect(row(page, laptop).getByRole("button")).toHaveText(["Move to in_hand"]);

  // The claim: one move. The laptop leaves the team's queue.
  await row(page, laptop).getByRole("button", { name: "Move to in_hand" }).click();
  await expect(page.getByRole("status")).toContainText("APPLIED");
  await expect(mine(page)).toHaveText([monitor]);

  // It is sam's now: in Waiting on me, named by the claim field, with
  // the claimant's moves in the Type's order.
  await page.getByRole("tab", { name: /Waiting on me/ }).click();
  await expect(row(page, laptop)).toContainText("equipment_request@1 · in_hand");
  await expect(row(page, laptop)).toContainText("f_handler is you");
  await expect(row(page, laptop).getByRole("button")).toHaveText(["Move to fulfilled", "Move to declined", "Move to queue"]);

  // lea is a desk agent too. The laptop is in neither of her tabs, and
  // on its own page the kernel refuses her the decision.
  await openInbox(page, "lea");
  await page.getByLabel("Order").selectOption("Newest waiting first");
  await expect(page.getByRole("tab", { name: /Waiting on me/ })).toHaveText(/\d/);
  await expect(inbox(page)).toHaveAttribute("aria-busy", "false");
  await expect(row(page, laptop)).toHaveCount(0);
  await page.getByRole("tab", { name: /desk_agent/ }).click();
  await expect(mine(page)).toHaveText([monitor]);
  await page.goto(`/#/items/${idOf(first!)}`);
  await page.getByRole("button", { name: "Move to fulfilled" }).click();
  await expect(page.getByRole("alert")).toContainText("NOT_ALLOWED");
  expect((await api(baseURL!, "lea").getItem({ name: first!.name })).item?.state).toBe("in_hand");

  // sam releases it: back in the team's queue, as its newest arrival,
  // which is the end of the queue when the oldest is served first.
  await openInbox(page, "sam");
  await page.getByLabel("Order").selectOption("Newest waiting first");
  await row(page, laptop).getByRole("button", { name: "Move to queue" }).click();
  await page.getByRole("tab", { name: /desk_agent/ }).click();
  await expect(mine(page)).toHaveText([laptop, monitor]);
  await expect(row(page, laptop).getByRole("button")).toHaveText(["Move to in_hand"]);

  const got = (await api(baseURL!, "sam").getItem({ name: first!.name })).item;
  expect(got?.state).toBe("queue");
  expect(got?.fields?.f_handler ?? null).toBeNull();
});

test("sam sends kim's incident back to her: in her Inbox, labelled; she answers on its page; it leaves", async ({ page, baseURL }) => {
  const title = `Scanner offline ${run}`;
  const { incident } = await seedSentBack(baseURL!, run, title);
  const row = (p: Page) => inbox(p).locator("> li").filter({ hasText: title });

  await openInbox(page, "kim");
  // Her one tab; her row, oldest waiting first, found by its title.
  await expect(page.getByRole("tab")).toHaveCount(1);
  await expect(row(page)).toContainText("incident@6 · waiting_for_customer");
  await expect(row(page)).toContainText("asked by customer:kim");
  await expect(row(page)).toContainText("sent back to you");
  // She answers by replying, not by a move: no button, a link to the incident.
  await expect(row(page).getByRole("button")).toHaveCount(0);
  await row(page).getByRole("link", { name: "Open it to answer" }).click();
  await expect(page.getByRole("heading", { name: title })).toBeVisible();

  // Her reply, written inside the incident.
  await page.goto(`/#/new?in=${idOf(incident)}`);
  await page.getByLabel("Type", { exact: true }).selectOption("reply");
  await page.getByLabel("f_body").fill(`It shows error 42 ${run}`);
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByRole("alert")).toHaveCount(0);

  // The incident's own rule brings it back to in_progress (a reaction, by
  // the worker); then it waits on the desk, not on her.
  const kim = api(baseURL!, "kim");
  await expect.poll(async () => (await kim.getItem({ name: incident.name })).item?.state, { timeout: 30_000 }).toBe("in_progress");
  await openInbox(page, "kim");
  await expect(inbox(page)).toHaveAttribute("aria-busy", "false");
  await expect(row(page)).toHaveCount(0);
});
