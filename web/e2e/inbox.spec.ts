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
//   4. kim, a customer, gets one sentence and no call.
//
// No Type on the stack has a claimed state yet, so claiming and releasing
// are shown against a fake kernel in src/pages/InboxPage.test.tsx.
//
// Every journey narrows the Inbox to a place it has just made: the stack
// keeps what earlier runs and the scenario left waiting.
//
// Run: docker compose up -d --build --wait, then `npm run e2e` in web/.
import { expect, test, type Page } from "@playwright/test";
import { api, cancelAll, idOf, seedApprovalQueue, seedDeskQueue, seedEmptyPlace } from "./seed";
import type { Item } from "../src/gen/kernel/v1/kernel_pb";

const run = Date.now().toString(36);

// What the journeys left waiting: canceled when they are done, so that
// the other journeys' Inboxes are as they found them.
const left: Item[] = [];
test.afterAll(async ({ baseURL }) => {
  await cancelAll(baseURL!, left);
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

test("kim, a customer: one sentence, and the page asks the kernel nothing", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Act as").selectOption("kim");
  await expect(page.getByRole("banner")).toContainText("acting as kim");
  // Only what the Inbox itself asks counts: the page before it has read.
  await page.waitForLoadState("networkidle");
  const calls: string[] = [];
  page.on("request", (req) => {
    if (req.url().includes("/api/")) calls.push(req.url().split("/").pop()!);
  });
  await page.getByRole("link", { name: "Inbox" }).click();
  await expect(page.getByRole("heading", { name: "Inbox" })).toBeVisible();
  await expect(page.getByText("Nothing waits on you here yet.")).toBeVisible();
  await expect(page.getByRole("tab")).toHaveCount(0);
  await page.waitForLoadState("networkidle");
  expect(calls).toEqual([]);
});
