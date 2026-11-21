// ana asks for time off; bob, her manager, decides from his inbox; hana
// (HR) sees the balances.
//
//   - ana: My leave shows her balance; a request whose last day is before
//     its first is refused in plain words; she fixes it and sends it: it
//     waits for bob (the HR check names him);
//   - bob: Waiting for you lists it; the decision screen shows the request,
//     ana's balance and her other requests in the period; he approves with
//     a comment; ana's balance drops by its days;
//   - ana asks for more days than she has left: the form warns, she sends
//     anyway; bob's approval is refused for lack of days, in plain words;
//     he rejects it with a comment;
//   - the approved request's case page: the process map at Approved (the
//     steps before it done), and the timeline, the deduction included;
//   - hana: the balances across people;
//   - axe on each main screen.
//
// The stack runs vacation_request on a dev clock (a day lasts 2 s): a
// submitted request expires after 10 s, so bob's tab is open before ana
// sends, and he decides at once. A slow machine can lose that race; the
// expiry then shows as "Expired" where "Approved" was expected.
//
// Run against the stack: docker compose up -d --build --wait (or make
// scenario), then `npm run e2e` in apps/poc.
import { expect, test, type Page } from "@playwright/test";
import { futureWeek, seedLeave } from "./seed";
import { cellOf, expectAccessible, refreshUntil, signIn } from "./support";

const run = `v${Date.now().toString(36)}`;
const beach = `Beach ${run}`;
const tooLong = `Long trip ${run}`;

// Requests in the Requests area, from the apps' navigation.
async function openRequests(page: Page): Promise<void> {
  await page.getByRole("navigation", { name: "Apps" }).getByRole("link", { name: "Requests" }).click();
}

test("ana asks for time off, bob decides, hana sees the balances", async ({ browser, baseURL }) => {
  await seedLeave(baseURL!, "user:ana", 10, ["user:bob"]);
  const week = futureWeek();

  const ana = await (await browser.newContext({ baseURL })).newPage();
  const bob = await (await browser.newContext({ baseURL })).newPage();

  // bob waits on his inbox (a request expires 10 s after it is sent).
  await signIn(bob, "bob");
  await openRequests(bob);
  await bob.getByRole("link", { name: "Waiting for you" }).click();
  await expect(bob.getByRole("heading", { level: 1, name: "Waiting for you" })).toBeVisible();

  // ana: My leave, her balance.
  await signIn(ana, "ana");
  await openRequests(ana);
  await expect(ana.getByRole("heading", { level: 1, name: "My leave" })).toBeVisible();
  await expect(ana.getByRole("link", { name: "My leave" })).toHaveAttribute("aria-current", "page");
  const balance = ana.getByRole("region", { name: "Your leave balance" });
  await expect(balance).toContainText("10 days left");
  await expect(balance).toContainText("of 10");
  await expectAccessible(ana, "My leave");

  // The form: dates that make no sense are refused, in plain words.
  await ana.getByRole("link", { name: "Request time off" }).click();
  await expect(ana.getByRole("heading", { level: 1, name: "Request time off" })).toBeVisible();
  await ana.getByLabel("First day").fill(week.plus(4));
  await ana.getByLabel("Last day").fill(week.monday);
  await ana.getByLabel("Days").fill("3");
  await ana.getByLabel("Leave type").selectOption("annual");
  await ana.getByLabel("Reason (optional)").fill(beach);
  await ana.getByRole("button", { name: "Send for approval" }).click();
  await expect(ana.getByRole("alert")).toContainText("The last day is before the first day.");
  await expectAccessible(ana, "the form, with a refusal");

  // Fixed: Monday to Wednesday, three days (the suggestion).
  await ana.getByLabel("First day").fill(week.monday);
  await ana.getByLabel("Last day").fill(week.plus(2));
  await expect(ana.getByLabel("Days")).toHaveValue("3");
  await ana.getByRole("button", { name: "Send for approval" }).click();
  await expect(ana.getByRole("heading", { level: 1, name: "Leave request" })).toBeVisible();
  await refreshUntil(ana, async () => {
    await expect(ana.getByLabel("Status")).toHaveText("Waiting for bob");
  });
  const approvedCase = ana.url();

  // bob: the inbox, then the decision, at once.
  const inbox = bob.getByRole("table", { name: "Requests waiting for you" });
  const row = inbox.getByRole("row").filter({ hasText: beach });
  await refreshUntil(bob, async () => {
    await expect(row).toHaveCount(1);
  });
  await expect(await cellOf(inbox, row, /^Requester/)).toHaveText("ana");
  await expect(await cellOf(inbox, row, /^Days/)).toHaveText("3");
  await expect(await cellOf(inbox, row, /^Waiting/)).toHaveText(/less than a minute|\d+ min/);
  await row.getByRole("link", { name: "Review ana's request" }).click();
  await expect(bob.getByRole("heading", { level: 1, name: "ana's request" })).toBeVisible();
  await expect(bob.getByRole("region", { name: "ana's leave balance" })).toContainText("10 days left");
  await expect(bob.getByRole("region", { name: "ana's other requests in this period" })).toContainText("None");
  await bob.getByLabel("Comment").fill(`Enjoy ${run}`);
  await bob.getByRole("button", { name: "Approve" }).click();
  await expect(bob.getByRole("status").filter({ hasText: "You approved ana's request." })).toBeVisible();
  await expect(bob.getByRole("heading", { level: 1, name: "Waiting for you" })).toBeVisible();
  await expect(inbox.getByRole("row").filter({ hasText: beach })).toHaveCount(0);
  await expectAccessible(bob, "the inbox");

  // ana: the balance drops; the request is approved.
  await ana.getByRole("link", { name: "My leave" }).click();
  await refreshUntil(ana, async () => {
    await expect(balance).toContainText("7 days left");
  });
  const mine = ana.getByRole("table", { name: "My requests" });
  await expect(await cellOf(mine, mine.getByRole("row").filter({ hasText: beach }), /^Status/)).toHaveText("Approved");

  // Too many days: the form warns; she sends anyway; bob's approval is refused.
  await ana.getByRole("link", { name: "Request time off" }).click();
  await ana.getByLabel("First day").fill(week.plus(7));
  await ana.getByLabel("Last day").fill(week.plus(18));
  await expect(ana.getByLabel("Days")).toHaveValue("10");
  await ana.getByLabel("Reason (optional)").fill(tooLong);
  await expect(ana.getByText("That's more than the 7 days you have left.")).toBeVisible();
  await ana.getByRole("button", { name: "Send for approval" }).click();
  await refreshUntil(ana, async () => {
    await expect(ana.getByLabel("Status")).toHaveText("Waiting for bob");
  });

  const longRow = inbox.getByRole("row").filter({ hasText: tooLong });
  await refreshUntil(bob, async () => {
    await expect(longRow).toHaveCount(1);
  });
  await longRow.getByRole("link", { name: "Review ana's request" }).click();
  await expect(bob.getByRole("region", { name: "ana's other requests in this period" })).toContainText("None");
  await bob.getByRole("button", { name: "Approve" }).click();
  await expect(bob.getByRole("alert")).toContainText(
    "ana doesn't have enough days left: this asks for 10 and ana has 7. Reject it, or ask HR to add days.",
  );
  await expectAccessible(bob, "the decision screen, with a refusal");
  await bob.getByLabel("Comment").fill("Not enough days left");
  await bob.getByRole("button", { name: "Reject" }).click();
  await expect(bob.getByRole("status").filter({ hasText: "You rejected ana's request." })).toBeVisible();

  // The approved request's case page: the map at its end, and the timeline.
  await ana.goto(approvedCase);
  await expect(ana.getByRole("heading", { level: 1, name: "Leave request" })).toBeVisible();
  await expect(ana.getByLabel("Status")).toHaveText("Approved");
  const process = ana.getByRole("region", { name: "Process" });
  await expect(process.getByRole("img", { name: /^Process map/ })).toBeVisible();
  await process.getByText("The process as a list").click();
  const steps = process.getByRole("list", { name: "Steps" });
  await expect(steps.getByRole("listitem").filter({ hasText: /^Draft/ })).toContainText("Done");
  await expect(steps.getByRole("listitem").filter({ hasText: /^Waiting for approval/ })).toContainText("Done");
  await expect(steps.getByRole("listitem").filter({ hasText: /^Approved/ })).toContainText("Current step");
  await expect(steps.getByRole("listitem").filter({ hasText: /^Rejected/ })).not.toContainText("Done");
  await expect(steps.getByRole("listitem").filter({ hasText: /^Waiting for approval/ })).toContainText("Reminder to the approver after 2 days");
  const timeline = ana.getByRole("region", { name: "Timeline" });
  await refreshUntil(ana, async () => {
    await expect(timeline).toContainText("Email sent to you: approved");
  });
  for (const said of [
    "You created the request",
    "You sent it for approval",
    "The HR check named bob as approver",
    "Linked to your leave balance",
    "3 days taken from your balance (3 used)",
    "bob approved it",
    `bob commented: "Enjoy ${run}"`,
  ])
    await expect(timeline).toContainText(said);
  await expectAccessible(ana, "the case page");

  // The refused request's page says why it was not approved.
  await ana.getByRole("link", { name: "My leave" }).click();
  const longMine = mine.getByRole("row").filter({ hasText: tooLong });
  await expect(await cellOf(mine, longMine, /^Status/)).toHaveText("Rejected");
  await longMine.getByRole("link").first().click();
  await expect(ana.getByRole("region", { name: "Timeline" })).toContainText("bob tried to approve it: not enough days left");
  await expect(ana.getByRole("region", { name: "Timeline" })).toContainText('bob commented: "Not enough days left"');

  // hana: the balances across people.
  const hana = await (await browser.newContext({ baseURL })).newPage();
  await signIn(hana, "hana");
  await openRequests(hana);
  await hana.getByRole("link", { name: "Balances" }).click();
  await expect(hana.getByRole("heading", { level: 1, name: "Leave balances" })).toBeVisible();
  const balances = hana.getByRole("table", { name: "Open leave balances" });
  const anaRow = balances.getByRole("row").filter({ hasText: /^\s*ana/ });
  await expect(anaRow).toHaveCount(1);
  await expect(await cellOf(balances, anaRow, /^Allowance/)).toHaveText("10");
  await expect(await cellOf(balances, anaRow, /^Used/)).toHaveText("3");
  await expect(await cellOf(balances, anaRow, /^Left/)).toHaveText("7");
  await expectAccessible(hana, "the balances");

  // ana has no Balances tab.
  await expect(ana.getByRole("link", { name: "Balances" })).toHaveCount(0);
});
