// sam works an incident on the service desk.
//
//   - the queue: incidents sorted by resolution due, wall-clock countdowns,
//     a P1 whose first response breaches (its goal records it) shown as
//     Breached, and in the Breached view;
//   - the incident: requester, assign to himself, an internal note (first
//     response still running), a public reply (first response Met, as the
//     goal records it), wait for the customer (resolution Paused), resolve
//     with a resolution (resolution Met), and the goals' timeline;
//   - axe on the queue and the incident.
//
// Run against the stack: docker compose up -d --build --wait (or make
// scenario), then `npm run e2e` in apps/poc.
import { expect, test } from "@playwright/test";
import { hours, seedDesk, seedRequest } from "./seed";
import { cellOf, expectAccessible, refreshUntil, signIn } from "./support";

const run = `d${Date.now().toString(36)}`;
const deskName = `E2E desk ${run}`;
const vpn = `VPN drops ${run}`;
const mail = `Mail down ${run}`;

test("sam works an incident", async ({ page, baseURL }) => {
  // A desk whose P1 first response is due 3 s after creation: it breaches
  // while sam looks at the queue (well under 10 s of waiting).
  const desk = await seedDesk(baseURL!, run, deskName, {
    firstResponse: [3, hours.firstResponse[1], hours.firstResponse[2], hours.firstResponse[3]],
    resolution: hours.resolution,
  });
  await seedRequest(baseURL!, "kim", desk, vpn, "P3"); // resolution due in 4 h
  await seedRequest(baseURL!, "lee", desk, mail, "P1"); // resolution due in 2 h, first response in 3 s

  await signIn(page, "sam");
  await expect(page).toHaveURL(/#\/desk/);
  await page.getByRole("link", { name: deskName }).click();

  // The queue: all open, soonest resolution first.
  await expect(page.getByRole("heading", { level: 1, name: deskName })).toBeVisible();
  await expect(page.getByRole("link", { name: "All open" })).toHaveAttribute("aria-current", "page");
  const table = page.getByRole("table", { name: "Incidents" });
  const rows = table.getByRole("row");
  await expect(rows.filter({ hasText: vpn })).toHaveCount(1);
  const titles = await rows.allTextContents();
  expect(titles.findIndex((t) => t.includes(mail))).toBeLessThan(titles.findIndex((t) => t.includes(vpn)));
  await expect(table.getByRole("columnheader", { name: /Resolution due \(wall clock\)/ })).toBeVisible();
  const vpnRow = rows.filter({ hasText: vpn });
  await expect(await cellOf(table, vpnRow, /^Resolution due/)).toHaveText(/^in 3 h \d+ min$/);
  await expect(await cellOf(table, vpnRow, /^Requester/)).toHaveText("kim");
  await expect(await cellOf(table, vpnRow, /^Assignee/)).toHaveText("Unassigned");

  // The P1's first response breaches: the goal records it, the queue shows it.
  const mailRow = rows.filter({ hasText: mail });
  await refreshUntil(page, async () => {
    await expect(await cellOf(table, mailRow, /^First response due/)).toHaveText("Breached");
  });
  await expectAccessible(page, "the queue");
  await page.getByRole("link", { name: "Breached" }).click();
  await expect(table.getByRole("row").filter({ hasText: mail })).toHaveCount(1);
  await expect(table.getByRole("row").filter({ hasText: vpn })).toHaveCount(0);
  await page.getByRole("link", { name: "All open" }).click();

  // The incident.
  await table.getByRole("link", { name: vpn }).click();
  await expect(page.getByRole("heading", { level: 1, name: vpn })).toBeVisible();
  await expect(page.getByLabel("Requester")).toHaveText("kim");
  await expect(page.getByLabel("Status")).toHaveText("New");
  const levels = page.getByRole("region", { name: "Service levels" });
  const firstResponse = levels.getByRole("listitem").filter({ hasText: "First response" });
  const resolution = levels.getByRole("listitem").filter({ hasText: "Resolution" });
  await expect(firstResponse).toContainText(/in 2 h \d+ min \(wall clock\)/);
  await expect(resolution).toContainText(/in 3 h \d+ min \(wall clock\)/);
  // The customer's description is her first message.
  await expect(page.getByRole("article", { name: /^Reply by kim · Customer/ })).toContainText(`About ${vpn}: please help.`);

  // Assign to himself.
  await page.getByRole("button", { name: "Assign to me" }).click();
  await expect(page.getByLabel("Assignee")).toHaveValue("user:sam");

  // An internal note answers nothing.
  const note = `note-${run}: check the gateway logs`;
  await page.getByRole("tab", { name: "Internal note" }).click();
  await expect(page.getByText("Only staff can see this")).toBeVisible();
  await page.getByRole("textbox", { name: "Internal note" }).fill(note);
  await page.getByRole("button", { name: "Add note" }).click();
  await expect(page.getByRole("article", { name: /^Internal note by You/ })).toContainText(note);
  await expect(firstResponse).not.toContainText("Met");

  // A public reply is the first response: met, as the goal records it.
  const reply = `reply-${run}: we are looking into it`;
  await page.getByRole("tab", { name: "Reply to customer" }).click();
  await expect(page.getByText("kim will see this")).toBeVisible();
  await page.getByRole("textbox", { name: "Reply" }).fill(reply);
  await page.getByRole("button", { name: "Send reply" }).click();
  await expect(page.getByRole("article", { name: /^Reply by You/ })).toContainText(reply);
  await refreshUntil(page, async () => {
    await expect(firstResponse).toContainText("Met");
  });
  await expectAccessible(page, "the incident");

  // Work, then wait for the customer: resolution pauses.
  await page.getByRole("button", { name: "Start work" }).click();
  await expect(page.getByLabel("Status")).toHaveText("In progress");
  await page.getByRole("button", { name: "Wait for customer" }).click();
  await expect(page.getByLabel("Status")).toHaveText("Waiting for customer");
  await refreshUntil(page, async () => {
    await expect(resolution).toContainText("Paused");
  });

  // Resolve: the resolution is asked for first.
  await page.getByRole("button", { name: "Resolve" }).click();
  const dialog = page.getByRole("dialog", { name: "Resolve" });
  await dialog.getByRole("combobox", { name: "Resolution" }).selectOption("fixed");
  await dialog.getByRole("button", { name: "Resolve" }).click();
  await expect(page.getByLabel("Status")).toHaveText("Resolved");
  await refreshUntil(page, async () => {
    await expect(resolution).toContainText("Met");
  });

  // The goals' timeline.
  const timeline = page.getByRole("region", { name: "Service level timeline" });
  const resolutionLife = timeline.getByRole("list", { name: "Resolution" });
  await expect(resolutionLife.getByRole("listitem")).toHaveText([/^Started/, /^Paused/, /^Met/]);
  await expect(timeline.getByRole("list", { name: "First response" }).getByRole("listitem")).toHaveText([/^Started/, /^Met/]);

  // Back on the queue, the resolved incident has left the open views.
  await page.getByRole("link", { name: "Queue" }).click();
  await expect(page.getByRole("table", { name: "Incidents" }).getByRole("row").filter({ hasText: vpn })).toHaveCount(0);
});
