// The browser smoke test: three behaviours, each through the generic UI
// (no screen knows a Type), against the compose stack.
//
//   1. as ana: create a ticket from the form built from GetType, move it.
//   2. as bob: approve ana's vacation request from the inbox.
//   3. as lea: drag a card on a sprint's board; the order holds in the
//      container too.
//
// Run: docker compose up -d --build --wait, then `npm run e2e` in web/.
import { expect, test, type Page } from "@playwright/test";
import { api, boardOrder, containerOrder, idOf, seedProject, seedVacation } from "./seed";

// Titles are unique per run: the stack keeps earlier runs' Items.
const run = Date.now().toString(36);

async function actAs(page: Page, name: string) {
  await page.goto("/");
  await page.getByLabel("Act as").selectOption(name);
  // The dev banner says who is acting, and that it is dev-only.
  await expect(page.getByRole("banner")).toContainText(`acting as ${name}`);
  await expect(page.getByRole("banner")).toContainText(/dev/i);
}

test("ana creates a ticket and moves it", async ({ page, baseURL }) => {
  const title = `printer on fire ${run}`;
  await actAs(page, "ana");

  await page.getByRole("link", { name: "New Item" }).click();
  await page.getByLabel("Type", { exact: true }).selectOption("ticket");
  // The form is the ticket's fields, from GetType; the latest version.
  await page.getByLabel("f_title").fill(title);
  await page.getByLabel("f_customer_id").fill("cust-good");
  await page.getByLabel("f_amount").fill("100");
  await page.getByRole("button", { name: "Create" }).click();

  // The new Item's page.
  await expect(page.getByRole("heading", { name: title })).toBeVisible();
  await expect(page.getByTestId("item-state")).toHaveText("triage");

  await page.getByRole("button", { name: "Move to open" }).click();
  await expect(page.getByRole("status")).toContainText("APPLIED");
  await expect(page.getByTestId("item-state")).toHaveText("open");

  // A refusal is shown with its code and reason: in_progress needs an
  // assignee (the guard), and the UI offered the move anyway.
  await page.getByRole("button", { name: "Move to in_progress" }).click();
  await expect(page.getByRole("alert")).toContainText("FAILED_PRECONDITION");
  await expect(page.getByRole("alert")).toContainText("GUARD_FAILED");
  await expect(page.getByTestId("item-state")).toHaveText("open");

  // The history shows what happened: created, and moved once (a refused
  // move writes no event). The ticket's hooks add their own events in
  // their own time, so the list is not counted.
  const history = page.getByRole("list", { name: "History" }).getByRole("listitem");
  await expect(history.filter({ hasText: /created/i })).toHaveCount(1);
  await expect(history.filter({ hasText: /transitioned/i })).toHaveCount(1);

  // And the server agrees.
  const id = page.url().split("/").pop()!;
  const got = await api(baseURL!, "ana").getItem({ name: `namespaces/acme/items/${id}` });
  expect(got.item?.state).toBe("open");
  expect(got.item?.fields?.f_title).toBe(title);
});

test("bob approves ana's vacation request from the inbox", async ({ page, baseURL }) => {
  const comment = `skiing ${run}`;
  const request = await seedVacation(baseURL!, comment);

  // ana does not find it in her inbox: she is not its approver.
  await actAs(page, "ana");
  await page.getByRole("link", { name: "Inbox" }).click();
  await expect(page.getByRole("heading", { name: "Inbox" })).toBeVisible();
  await expect(page.getByRole("listitem").filter({ hasText: comment })).toHaveCount(0);

  await actAs(page, "bob");
  await page.getByRole("link", { name: "Inbox" }).click();
  const row = page.getByRole("listitem").filter({ hasText: comment });
  await expect(row).toBeVisible();
  // The row offers the approver's moves only.
  await expect(row.getByRole("button", { name: "Move to approved" })).toBeVisible();
  await expect(row.getByRole("button", { name: "Move to rejected" })).toBeVisible();
  await expect(row.getByRole("button", { name: "Move to withdrawn" })).toHaveCount(0);

  // Approving is held while the leave balance is deducted; the UI waits.
  // The inbox keeps the last answer on the page, since the row itself
  // goes away.
  await row.getByRole("button", { name: "Move to approved" }).click();
  await expect(page.getByRole("status")).toContainText("APPLIED");
  // Refreshed after the action: the request has left the inbox.
  await expect(page.getByRole("listitem").filter({ hasText: comment })).toHaveCount(0);

  const got = await api(baseURL!, "bob").getItem({ name: request.name });
  expect(got.item?.state).toBe("approved");
});

test("lea drags a card on a sprint's board", async ({ page, baseURL }) => {
  const project = await seedProject(baseURL!, run);
  const [one, two, three] = project.stories.map((s) => String(s.fields?.f_summary)) as [string, string, string];
  const outsider = String(project.outsider.fields?.f_summary);

  await actAs(page, "lea");
  // From the sprint's page: the board of what links to it by `sprint`.
  await page.goto(`/#/items/${idOf(project.sprint)}`);
  await page.getByRole("link", { name: "Board: sprint_issues" }).click();

  const cards = page.getByRole("list", { name: "Board" }).getByRole("listitem");
  await expect(cards).toHaveText([new RegExp(one), new RegExp(two), new RegExp(three)]);

  // Drag the last card onto the top edge of the first: it goes before it.
  await cards.nth(2).dragTo(cards.nth(0), { targetPosition: { x: 20, y: 3 } });
  await expect(cards).toHaveText([new RegExp(three), new RegExp(one), new RegExp(two)]);

  // The order is the server's: it survives a reload...
  await page.reload();
  await expect(cards).toHaveText([new RegExp(three), new RegExp(one), new RegExp(two)]);
  expect(await boardOrder(baseURL!, project.sprint)).toEqual([three, one, two]);
  // ...and it is the container's order: the card went right before story
  // one in the space, above the story that is not on the board.
  expect(await containerOrder(baseURL!, project.space)).toEqual([three, one, outsider, two]);

  // The same move without a mouse.
  await page.getByRole("button", { name: `Move ${two} up` }).click();
  await expect(cards).toHaveText([new RegExp(three), new RegExp(two), new RegExp(one)]);
  expect(await containerOrder(baseURL!, project.space)).toEqual([three, two, one, outsider]);
});
