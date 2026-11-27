// The browser smoke test: three behaviours, each through the generic UI
// (no screen knows a Type), against the compose stack.
//
//   1. as ana: create a ticket from the form built from GetType, move it.
//   2. as bob: approve ana's vacation request from the inbox.
//   3. as lea: drag a card on a sprint's board; the order holds in the
//      container too.
//
//   4. as lea: set a sprint's end instant through the generic form (a
//      datetime field) and read it back, on the page and from the server.
//   5. as lea: give a story labels through the generic form (a multi-value
//      field, shown as chips), change them, and read them back.
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
  // The row offers the approver's moves only: the first as its main
  // button, the other behind More moves (the Inbox for people, PR 2b).
  await expect(row.getByRole("button", { name: "Move to approved" })).toBeVisible();
  await row.getByRole("button", { name: "More moves" }).click();
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

// A datetime field in the generic form (Jira part 2, step 1). The form
// edits the instant on the browser's clock and the server keeps it in UTC,
// so the browser's zone is fixed here: New York, UTC-4 in June and UTC-5 in
// December.
test.describe("a datetime field", () => {
  test.use({ timezoneId: "America/New_York" });

  test("lea sets a sprint's end instant and reads it back", async ({ page, baseURL }) => {
    const name = `Sprint with an end ${run}`;
    await actAs(page, "lea");

    await page.getByRole("link", { name: "New Item" }).click();
    // The latest version of sprint has f_end_at; nothing here knows that.
    await page.getByLabel("Type", { exact: true }).selectOption("sprint");
    await page.getByLabel("f_name").fill(name);
    const end = page.getByLabel("f_end_at");
    await expect(end).toHaveAttribute("type", "datetime-local");
    await end.fill("2030-06-01T12:30:15");
    await page.getByRole("button", { name: "Create" }).click();

    // The new Item's page: the form shows the instant on the browser's
    // clock again, and says which instant that is in UTC.
    await expect(page.getByTestId("item-state")).toHaveText("planned");
    await expect(page.getByLabel("f_end_at")).toHaveValue("2030-06-01T12:30:15");
    await expect(page.getByLabel("f_end_at")).toHaveAttribute("title", "2030-06-01T16:30:15.000Z");
    // The Type says what the active state will do with it.
    await page.getByRole("button", { name: "Move to active" }).click();
    await expect(page.getByTestId("item-state")).toHaveText("active");
    await expect(page.getByRole("button", { name: "Move to closed" }).locator("xpath=ancestor::li")).toContainText(
      "made by the timer at f_end_at",
    );

    // The server holds the instant in UTC, in its one form.
    const id = page.url().split("/").pop()!;
    const item = `namespaces/acme/items/${id}`;
    expect((await api(baseURL!, "lea").getItem({ name: item })).item?.fields?.f_end_at).toBe("2030-06-01T16:30:15.000000Z");

    // Moving the end through the same form: winter time now.
    await page.getByLabel("f_end_at").fill("2030-12-01T09:00:05");
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page.getByLabel("f_end_at")).toHaveValue("2030-12-01T09:00:05");
    await expect
      .poll(async () => (await api(baseURL!, "lea").getItem({ name: item })).item?.fields?.f_end_at)
      .toBe("2030-12-01T14:00:05.000000Z");
    // Years away: the sprint is still running.
    expect((await api(baseURL!, "lea").getItem({ name: item })).item?.state).toBe("active");
  });
});

// A multi-value field in the generic form (Jira part 2, step 2): chips. An
// edit is sent as a set operation, so the server's set is the old one with
// one member removed and one added, sorted.
test("lea labels a story and changes the labels", async ({ page, baseURL }) => {
  const summary = `Labelled in the browser ${run}`;
  await actAs(page, "lea");

  await page.getByRole("link", { name: "New Item" }).click();
  // The latest version of story has f_labels; nothing here knows that.
  await page.getByLabel("Type", { exact: true }).selectOption("story");
  await page.getByLabel("f_summary").fill(summary);
  const labels = page.getByRole("group", { name: "f_labels" });
  for (const label of ["sso", "backend"]) {
    await labels.getByRole("textbox", { name: "Add to f_labels" }).fill(label);
    await labels.getByRole("button", { name: "Add to f_labels" }).click();
  }
  await expect(labels.getByRole("listitem")).toHaveText([/backend/, /sso/]);
  await page.getByRole("button", { name: "Create" }).click();

  // The new Item's page shows the set as the server holds it.
  await expect(page.getByRole("heading", { name: summary })).toBeVisible();
  const shown = page.getByRole("group", { name: "f_labels" });
  await expect(shown.getByRole("listitem")).toHaveText([/backend/, /sso/]);
  const id = page.url().split("/").pop()!;
  const item = `namespaces/acme/items/${id}`;
  expect((await api(baseURL!, "lea").getItem({ name: item })).item?.fields?.f_labels).toEqual(["backend", "sso"]);

  // Someone else adds a label meanwhile, through the API.
  await api(baseURL!, "sam").updateItem({ name: item, requestId: `smoke-${run}-sam`, fields: { f_labels: { add: ["from-sam"] } } });

  // lea, whose page does not show it yet, removes one and adds one: her
  // save changes those two, and sam's label stays.
  await shown.getByRole("button", { name: "Remove sso" }).click();
  await shown.getByRole("textbox", { name: "Add to f_labels" }).fill("urgent");
  await shown.getByRole("textbox", { name: "Add to f_labels" }).press("Enter");
  await page.getByRole("button", { name: "Save" }).click();
  await expect
    .poll(async () => (await api(baseURL!, "lea").getItem({ name: item })).item?.fields?.f_labels)
    .toEqual(["backend", "from-sam", "urgent"]);
  await expect(page.getByRole("group", { name: "f_labels" }).getByRole("listitem")).toHaveText([/backend/, /from-sam/, /urgent/]);
});
