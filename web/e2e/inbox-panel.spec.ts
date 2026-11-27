// The Inbox for people, PR 2c, in the browser against the compose stack:
// the side panel, several rows at once, the keys.
//
//   1. bob opens a ticket's panel from its title and approves it there: the
//      move asks for its two required fields in place; the answer is said
//      in the page's status line, and the panel goes to the next ticket.
//   2. sam selects two equipment requests in the desk agents' queue and
//      takes both from the bar: one result per row, and both are then
//      his, being handled.
//   3. sam on the list: J and K move between rows, X selects, Enter opens
//      the panel, P makes the main move (Take it).
//
// Run: docker compose up -d --build --wait, then `npm run e2e` in web/.
import { expect, test, type Page } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { api, cancelAll, idOf, seedApprovalQueue, seedLabelledEquipmentRequests } from "./seed";
import type { Item } from "../src/gen/kernel/v1/kernel_pb";

const run = Date.now().toString(36);
const left: Item[] = [];
const taken: Item[] = [];
test.afterAll(async ({ baseURL }) => {
  await cancelAll(baseURL!, left);
  // sam hands back what he took; ana withdraws it. Failures are not the journeys'.
  const move = (who: string, it: Item, to: string) => api(baseURL!, who).transitionItem({ name: it.name, requestId: randomUUID(), toState: to });
  await Promise.allSettled(taken.map((it) => move("sam", it, "queue")));
  await Promise.allSettled(taken.map((it) => move("ana", it, "withdrawn")));
});

async function openInbox(page: Page, name: string) {
  await page.goto("/");
  await page.getByLabel("Act as").selectOption(name);
  await expect(page.getByRole("banner")).toContainText(`acting as ${name}`);
  await page.getByRole("link", { name: "Inbox" }).click();
  await expect(page.getByRole("heading", { name: "Inbox", level: 1 })).toBeVisible();
}

const inbox = (page: Page) => page.getByRole("region", { name: "Inbox" });

test("bob approves a ticket from its panel: two required fields asked in place; the panel goes to the next", async ({ page, baseURL }) => {
  const queue = await seedApprovalQueue(baseURL!, `${run}p`, 2);
  left.push(...queue.tickets);
  const [first, second] = [`Approve ${run}p #1`, `Approve ${run}p #2`];

  await openInbox(page, "bob");
  await page.getByLabel("Place").fill(idOf(queue.box));
  await page.getByRole("button", { name: "Apply" }).click();
  await inbox(page).getByRole("link", { name: first, exact: true }).click();

  const aside = page.getByRole("complementary", { name: first });
  await expect(aside).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`#/inbox\\?item=${idOf(queue.tickets[0]!)}$`));
  await expect(aside.getByRole("heading", { name: first })).toBeFocused();
  await expect(aside.getByRole("link", { name: "Open the full page" })).toHaveAttribute("href", `#/items/${idOf(queue.tickets[0]!)}`);

  await aside.getByRole("button", { name: "Move to approved" }).click();
  await aside.getByLabel("f_customer_id").fill("cust-good");
  await aside.getByLabel("f_amount").fill("100");
  await aside.getByRole("button", { name: /confirm/i }).click();

  await expect(page.getByRole("status")).toContainText(`${first}: Move to approved: APPLIED`);
  await expect(page.getByRole("complementary", { name: second })).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`#/inbox\\?item=${idOf(queue.tickets[1]!)}$`));
  await expect(page.getByRole("alert")).toHaveCount(0);
  expect((await api(baseURL!, "bob").getItem({ name: queue.tickets[0]!.name })).item?.state).toBe("approved");
});

test("sam takes two equipment requests at once from the bar: one result per row; both are his", async ({ page, baseURL }) => {
  const titles = [`Bulk monitor ${run}`, `Bulk headset ${run}`];
  const made = await seedLabelledEquipmentRequests(baseURL!, titles);
  taken.push(...made);

  await openInbox(page, "sam");
  await page.getByLabel("Order").selectOption("Newest waiting first");
  await page.getByRole("tab", { name: /Desk agent/ }).click();
  for (const t of titles) await page.getByRole("checkbox", { name: `Select ${t}` }).check();

  const bar = page.getByRole("region", { name: "Selected rows" });
  await expect(bar).toContainText("2 selected");
  await bar.getByRole("button", { name: "Take it (2)" }).click();

  const results = page.getByRole("list", { name: "Results" }).getByRole("listitem");
  await expect(results).toHaveText(titles.map((t) => `${t}: Take it: APPLIED`));
  const handled = inbox(page).getByRole("list", { name: "Being handled" });
  for (const t of titles) await expect(handled.locator("> li").filter({ hasText: t })).toContainText("You have it");
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("sam on the list: J and K move, X selects, Enter opens the panel, P takes it", async ({ page, baseURL }) => {
  const [older, newer] = [`Keys chair ${run}`, `Keys lamp ${run}`];
  const made = await seedLabelledEquipmentRequests(baseURL!, [older, newer]);
  taken.push(...made);

  await openInbox(page, "sam");
  await page.getByLabel("Order").selectOption("Newest waiting first");
  await page.getByRole("tab", { name: /Desk agent/ }).click();
  await expect(page.getByText("Keys: J and K to move · Enter to open · X to select · P for the main move")).toBeVisible();

  // Newest first: the lamp, then the chair.
  const lamp = inbox(page).getByRole("link", { name: newer, exact: true });
  const chair = inbox(page).getByRole("link", { name: older, exact: true });
  await lamp.focus();
  await page.keyboard.press("j");
  await expect(chair).toBeFocused();
  await page.keyboard.press("k");
  await expect(lamp).toBeFocused();

  await page.keyboard.press("x");
  await expect(page.getByRole("checkbox", { name: `Select ${newer}` })).toBeChecked();

  await page.keyboard.press("Enter");
  const aside = page.getByRole("complementary", { name: newer });
  await expect(aside).toBeVisible();
  await aside.getByRole("button", { name: "Close" }).click();
  await expect(lamp).toBeFocused();

  await page.keyboard.press("p");
  await expect(page.getByRole("status")).toContainText(`${newer}: Take it: APPLIED`);
  await expect(inbox(page).getByRole("list", { name: "Being handled" }).locator("> li").filter({ hasText: newer })).toContainText("You have it");
});
