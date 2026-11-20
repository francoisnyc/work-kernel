// The browser test of Jira part 2, step 3b: an incident's page, through the
// generic UI (no screen knows a Type), against the compose stack.
//
//   as sam, on ana's incident:
//   - its two goals are listed as contained Items, each running;
//   - its due instants (date-time fields only the kernel writes) say how
//     far away they are, labelled as wall-clock time: they are computed
//     in working time on a calendar, which the label must not suggest;
//   - the history's reaction records name the goal each reaction created,
//     as a link to it;
//   - the bug linked by caused_by shows its state, and its new state after
//     it moves: no reaction carries it, the page reads the linked Item.
//
// Run: docker compose up -d --build --wait, then `npm run e2e` in web/.
import { expect, test, type Page } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { api, idOf, seedIncident } from "./seed";

const run = Date.now().toString(36);

async function actAs(page: Page, name: string) {
  await page.goto("/");
  await page.getByLabel("Act as").selectOption(name);
  await expect(page.getByRole("banner")).toContainText(`acting as ${name}`);
}

test("sam reads an incident: its goals, its due instants, what the reactions did, the linked bug's state", async ({ page, baseURL }) => {
  const seeded = await seedIncident(baseURL!, run);
  await actAs(page, "sam");
  await page.goto(`/#/items/${idOf(seeded.incident)}`);
  await expect(page.getByRole("heading", { name: `VPN down ${run}` })).toBeVisible();
  await expect(page.getByTestId("item-state")).toHaveText("new");

  // The goals: contained Items like any other, of the package's Type.
  const contained = page.getByRole("list", { name: "Contained Items" }).getByRole("listitem");
  await expect(contained).toHaveCount(2);
  await expect(contained.filter({ hasText: /sla_goal@1 · running/ })).toHaveCount(2);

  // The due instants: the P3 targets are 3 h and 12 h on a calendar that
  // is always open, so the clock and the calendar agree here.
  const fields = page.getByRole("region", { name: "Fields" });
  await expect(fields.getByText(/in (2|3) h \(wall clock\)/)).toBeVisible();
  await expect(fields.getByText(/in (11|12) h \(wall clock\)/)).toBeVisible();
  // Only the kernel writes them: the form does not offer them for editing.
  await expect(page.getByLabel("f_due_resolution")).toBeDisabled();

  // The history: each start record links to the goal it created.
  const history = page.getByRole("list", { name: "History" }).getByRole("listitem");
  for (const [reaction, goal] of [
    ["incident:sla_start_first_response", seeded.firstResponse],
    ["incident:sla_start_resolution", seeded.resolution],
  ] as const) {
    const record = history.filter({ hasText: reaction });
    await expect(record).toHaveCount(1);
    await expect(record.getByRole("link")).toHaveAttribute("href", `#/items/${idOf(goal)}`);
  }

  // The linked bug, with its state.
  const links = page.getByRole("region", { name: "Links" }).getByRole("listitem");
  const cause = links.filter({ hasText: "caused_by" });
  await expect(cause).toHaveCount(1);
  await expect(cause).toContainText(`Gateway leaks memory ${run}`);
  await expect(cause).toContainText("todo");

  // sam starts on the bug, elsewhere: the incident's page, refreshed,
  // shows the bug's new state. The incident itself heard nothing.
  const sam = api(baseURL!, "sam");
  const before = (await sam.listItemEvents({ parent: seeded.incident.name, pageSize: 100 })).events.length;
  await sam.transitionItem({ name: seeded.bug.name, requestId: randomUUID(), toState: "in_progress" });
  await page.getByRole("button", { name: "Refresh" }).click();
  await expect(cause).toContainText("in_progress");
  await expect(cause).not.toContainText("todo");
  const after = (await sam.listItemEvents({ parent: seeded.incident.name, pageSize: 100 })).events.length;
  expect(after).toBe(before);

  // Someone who cannot see the bug sees a restricted link, and no state.
  await sam.updateItem({
    name: seeded.bug.name,
    requestId: randomUUID(),
    policy: { change: { case: "narrow", value: { principals: ["user:sam"] } } },
  });
  await actAs(page, "ana");
  await page.goto(`/#/items/${idOf(seeded.incident)}`);
  const hers = page.getByRole("region", { name: "Links" }).getByRole("listitem").filter({ hasText: "caused_by" });
  await expect(hers).toContainText(/restricted/i);
  await expect(hers).not.toContainText("in_progress");
});

// The etag step. ana's page of an incident is open; the kernel then writes
// on the incident (sam answers, so the first-response goal completes: an
// outcome record, and the due instant only the kernel writes is cleared).
// Nothing ana could have overwritten changed: her save, made WITHOUT
// refreshing, is accepted. (With the etag of Jira 2.3b it was refused
// ABORTED / STALE: the README's "known limit".) A real conflict is still
// refused: sam changes a field, and her next save without a refresh is
// STALE.
test("ana saves an incident's form after the kernel wrote on it, without refreshing; a real conflict is still STALE", async ({ page, baseURL }) => {
  const seeded = await seedIncident(baseURL!, `${run}e`);
  const ana = api(baseURL!, "ana");
  const sam = api(baseURL!, "sam");
  const name = seeded.incident.name;

  await actAs(page, "ana");
  await page.goto(`/#/items/${idOf(seeded.incident)}`);
  const fields = page.getByRole("region", { name: "Fields" });
  await expect(page.getByRole("heading", { name: `VPN down ${run}e` })).toBeVisible();
  await expect(fields.getByText(/in (2|3) h \(wall clock\)/)).toBeVisible(); // the first-response due instant is shown
  const opened = (await ana.getItem({ name })).item!;
  const eventsBefore = (await ana.listItemEvents({ parent: name, pageSize: 100 })).events.length;

  // sam answers in public: the kernel completes the first-response goal
  // and clears its due instant on the incident.
  await sam.createItem({
    parent: "namespaces/acme",
    requestId: randomUUID(),
    type: "namespaces/acme/types/comment/versions/2",
    container: name,
    fields: { f_body: "Looking into it", f_public: true },
  });
  await expect
    .poll(async () => (await ana.getItem({ name })).item?.fields?.f_due_first_response ?? null, { timeout: 30_000 })
    .toBeNull();
  const written = (await ana.getItem({ name })).item!;
  const eventsAfter = (await ana.listItemEvents({ parent: name, pageSize: 100 })).events.length;
  expect(eventsAfter).toBeGreaterThan(eventsBefore); // the kernel wrote on the incident
  expect(written.etag).toBe(opened.etag); // and its etag did not move

  // ana saves what she was shown, with no refresh.
  await page.getByLabel("f_impact").selectOption("minor");
  await fields.getByRole("button", { name: "Save" }).click();
  await expect(page.getByLabel("f_impact")).toHaveValue("minor");
  await expect(fields.getByRole("alert")).toHaveCount(0);
  await expect.poll(async () => (await ana.getItem({ name })).item?.fields?.f_impact).toBe("minor");
  // The page reloaded after the save: the cleared due instant is gone.
  await expect(page.getByLabel("f_due_first_response")).toHaveValue("");

  // A real conflict: sam changes the impact; ana, who has not refreshed,
  // saves another field.
  await sam.updateItem({ name, requestId: randomUUID(), fields: { f_impact: "extensive" } });
  await page.getByLabel("f_title").fill(`VPN down ${run}e, Paris`);
  await fields.getByRole("button", { name: "Save" }).click();
  const alert = fields.getByRole("alert");
  await expect(alert).toContainText("ABORTED");
  await expect(alert).toContainText("STALE");
  expect((await ana.getItem({ name })).item?.fields?.f_title).toBe(`VPN down ${run}e`);
});
