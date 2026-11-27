// The Inbox for people, PR 2b, in the browser against the compose stack:
// rows in words, Overdue then Waiting, a team tab's two sections.
//
//   1. sam's Waiting on me, narrowed to a desk with two new incidents
//      (incident@7): the P1, past its first response due, is in Overdue,
//      first, "due … ago" in amber; the P4 is in Waiting, "due in …". A row
//      says its summary, Type and state, who asked by name, and offers its
//      primary move ("Start"), the rest behind More moves.
//   2. sam's desk tab: his own request is in "Nobody has these yet" and
//      says why he cannot take it; the one lea took is in "Being handled",
//      "Lea has it", and sam's one move there is to hand it back.
//
// Run: docker compose up -d --build --wait, then `npm run e2e` in web/.
import { expect, test, type Page } from "@playwright/test";
import { cancelAll, clearOwnAndClaimed, idOf, seedOverdueDesk, seedOwnAndClaimed, type OwnAndClaimed } from "./seed";
import type { Item } from "../src/gen/kernel/v1/kernel_pb";

const run = Date.now().toString(36);
const left: Item[] = [];
let ownAndClaimed: OwnAndClaimed | undefined;
test.afterAll(async ({ baseURL }) => {
  await cancelAll(baseURL!, left);
  if (ownAndClaimed) await clearOwnAndClaimed(baseURL!, ownAndClaimed);
});

async function openInbox(page: Page, name: string) {
  await page.goto("/");
  await page.getByLabel("Act as").selectOption(name);
  await expect(page.getByRole("banner")).toContainText(`acting as ${name}`);
  await page.getByRole("link", { name: "Inbox" }).click();
  await expect(page.getByRole("heading", { name: "Inbox", level: 1 })).toBeVisible();
}

const inbox = (page: Page) => page.getByRole("region", { name: "Inbox" });
const section = (page: Page, name: string) => inbox(page).getByRole("list", { name });
const rowIn = (page: Page, name: string, title: string) => section(page, name).locator("> li").filter({ hasText: title });

test("sam's Waiting on me: the overdue incident first, in amber; the rest waiting; rows in words", async ({ page, baseURL }) => {
  const late = `Mail down ${run}`;
  const onTime = `Mouse broken ${run}`;
  const desk = await seedOverdueDesk(baseURL!, run, late, onTime);
  left.push(desk.late, desk.onTime);

  await openInbox(page, "sam");
  await page.getByLabel("Place").fill(idOf(desk.desk));
  await page.getByRole("button", { name: "Apply" }).click();

  await expect(inbox(page).getByRole("heading", { level: 2 })).toHaveText(["Overdue", "Waiting"]);
  await expect(section(page, "Overdue").locator("> li > a")).toHaveText([late]);
  await expect(section(page, "Waiting").locator("> li > a")).toHaveText([onTime]);

  const lateRow = rowIn(page, "Overdue", late);
  await expect(lateRow).toContainText("Priority: P1");
  await expect(lateRow).toContainText("incident · New");
  await expect(lateRow).toContainText("Ana asks");
  await expect(lateRow.locator(".overdue")).toHaveText(/^due \d+ (s|min) ago$/);
  await expect(rowIn(page, "Waiting", onTime)).toContainText(/due in \d+ h/);
  await expect(rowIn(page, "Waiting", onTime).locator(".overdue")).toHaveCount(0);

  // Start is the primary move; Cancel waits behind More moves.
  await expect(lateRow.getByRole("button")).toHaveText(["Start", "More moves"]);
  const more = lateRow.getByRole("button", { name: "More moves" });
  await expect(more).toHaveAttribute("aria-expanded", "false");
  await more.click();
  await expect(more).toHaveAttribute("aria-expanded", "true");
  await expect(lateRow.getByRole("button")).toHaveText(["Start", "More moves", "Cancel"]);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("sam's desk tab: his own request says why he cannot take it; lea's is being handled, Lea has it", async ({ page, baseURL }) => {
  const own = `Dock ${run}`;
  const held = `Chair ${run}`;
  ownAndClaimed = await seedOwnAndClaimed(baseURL!, own, held);

  await openInbox(page, "sam");
  await page.getByLabel("Order").selectOption("Newest waiting first");
  await page.getByRole("tab", { name: /Desk agent/ }).click();

  const ownRow = rowIn(page, "Nobody has these yet", own);
  await expect(ownRow).toContainText("You asked for this one");
  await expect(ownRow).toContainText("You can't handle your own request");
  await expect(ownRow.getByRole("button")).toHaveText(["Withdraw"]);
  await expect(rowIn(page, "Nobody has these yet", held)).toHaveCount(0);

  const heldRow = rowIn(page, "Being handled", held);
  await expect(heldRow).toContainText("Lea has it");
  await expect(heldRow).toContainText(/in hand (\d+ (s|min)|just arrived)/);
  await expect(heldRow.getByRole("button")).toHaveText(["Hand back"]);
  await expect(page.getByRole("alert")).toHaveCount(0);
});
