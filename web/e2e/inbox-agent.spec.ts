// The Inbox for people, PR 3b, in the browser against the compose stack:
// an agent's request, shown as one.
//
//   1. triage-bot, in a session for ana, gives ana's ticket a priority and
//      moves it to where it waits on the approvers: bob's row reads "Agent
//      request · for Ana"; its panel says "Triage agent, for Ana" and, under
//      the warning, what the agent wrote (the priority).
//   2. The portal's agent opened kim's incident for her; sam sent it back
//      to her: her row reads "Agent request · for you".
//
// Run: docker compose up -d --build --wait, then `npm run e2e` in web/.
import { expect, test, type Page } from "@playwright/test";
import { cancelAll, seedAgentSentBack, seedAgentTicket } from "./seed";
import type { Item } from "../src/gen/kernel/v1/kernel_pb";

const run = Date.now().toString(36);
const left: Item[] = [];
test.afterAll(async ({ baseURL }) => {
  await cancelAll(baseURL!, left);
});

async function openInbox(page: Page, name: string) {
  await page.goto("/");
  await page.getByLabel("Act as").selectOption(name);
  await expect(page.getByRole("banner")).toContainText(`acting as ${name}`);
  await page.getByRole("link", { name: "Inbox" }).click();
  await expect(page.getByRole("heading", { name: "Inbox", level: 1 })).toBeVisible();
}

const inbox = (page: Page) => page.getByRole("region", { name: "Inbox" });
const row = (page: Page, title: string) => inbox(page).locator("ul[aria-label] > li").filter({ hasText: title });

test("bob's row of ana's ticket that triage-bot moved for her: Agent request · for Ana; its panel shows what the agent wrote", async ({ page, baseURL }) => {
  const title = `Agent ticket ${run}`;
  left.push(await seedAgentTicket(baseURL!, title));

  await openInbox(page, "bob");
  await page.getByLabel("Order").selectOption("Newest waiting first");
  await expect(row(page, title)).toContainText("Agent request · for Ana");

  await inbox(page).getByRole("link", { name: title, exact: true }).click();
  const aside = page.getByRole("complementary", { name: title });
  await expect(aside).toContainText("Triage agent, for Ana");
  const wrote = aside.getByRole("region", { name: "What the agent wrote" });
  await expect(wrote).toContainText("Written by an agent. Check it before you decide.");
  await expect(wrote).toContainText("f_priority");
  await expect(wrote).toContainText("2");
  await expect(wrote).not.toContainText(title);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("kim's incident the portal's agent opened for her, sent back to her: Agent request · for you", async ({ page, baseURL }) => {
  const title = `Agent scanner ${run}`;
  await seedAgentSentBack(baseURL!, run, title);

  await openInbox(page, "kim");
  await expect(row(page, title)).toContainText("Agent request · for you");
  await expect(row(page, title)).toContainText("sent back to you");
  await expect(page.getByRole("alert")).toHaveCount(0);
});
