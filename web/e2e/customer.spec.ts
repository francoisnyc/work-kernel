// The browser test of Jira part 2, step 4a: a customer in the generic UI
// (no screen knows a Type), against the compose stack.
//
//   as kim, a customer:
//   - the picker names her a customer;
//   - the top level is the portals only;
//   - inside a portal she is offered only the Types it admits, and opens
//     an incident there, setting only the fields she may;
//   - her incident shows her fields and the replies, never the goals, the
//     note, the assignee or the deadlines, and no reaction record;
//   - she is offered the moves whose allowed names creator, and no other;
//   - lee's incident does not exist for her.
//   as sam: the same incident with everything, as before.
//
// Run: docker compose up -d --build --wait, then `npm run e2e` in web/.
import { expect, test, type Page } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { api, idOf } from "./seed";

const run = Date.now().toString(36);
const NS = "namespaces/acme";
const type = (id: string, version: number) => `${NS}/types/${id}/versions/${version}`;

async function actAs(page: Page, name: string) {
  await page.goto("/");
  await page.getByLabel("Act as").selectOption(name);
  await expect(page.getByRole("banner")).toContainText(`acting as ${name}`);
}

// A portal linked to a calendar and a policy (lea holds sla_admin), and
// lee's incident in it.
async function seedPortal(baseURL: string) {
  const lea = api(baseURL, "lea");
  const mk = async (typeName: string, fields: Record<string, unknown>, container = "") =>
    (await lea.createItem({ parent: NS, requestId: randomUUID(), type: typeName, fields: fields as never, container })).item!;
  const week = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"].map((d) => `${d} 00:00-24:00`);
  const cal = await mk(type("calendar", 1), { f_name: "always", f_week: week, f_zone: "UTC" });
  const targets: Record<string, string> = { f_name: "standard" };
  ["p1", "p2", "p3", "p4"].forEach((p, i) => {
    targets[`f_first_response_s_${p}`] = String(3600 * (i + 1));
    targets[`f_resolution_s_${p}`] = String(36000 * (i + 1));
  });
  const policy = await mk(type("sla_policy", 1), targets);
  const portal = await mk(type("desk", 2), { f_key: `IT-${run}`, f_name: `Portal ${run}` });
  for (const [linkType, target] of [["calendar", cal.name], ["sla_policy", policy.name]] as const) {
    await lea.linkItems({ name: portal.name, requestId: randomUUID(), linkType, target });
  }
  const lee = api(baseURL, "lee");
  const leesIncident = (
    await lee.createItem({ parent: NS, requestId: randomUUID(), type: type("incident", 3), container: portal.name,
      fields: { f_title: `lee's ${run}`, f_priority: "P3" } })
  ).item!;
  return { portal, leesIncident };
}

test("kim, a customer: her portal, her incident, its replies, and nothing else", async ({ page, baseURL }) => {
  const { portal, leesIncident } = await seedPortal(baseURL!);
  await actAs(page, "kim");
  await expect(page.getByLabel("Act as").locator("option:checked")).toHaveText("kim (customer)");

  // The top level: portals only.
  await page.goto("/#/items");
  const listed = page.getByRole("list", { name: "Results" }).getByRole("listitem");
  await expect(listed.filter({ hasText: `Portal ${run}` })).toHaveCount(1);
  for (const text of await listed.allTextContents()) expect(text).toContain("desk@2");

  // Inside the portal: only the incident is offered, and only her fields.
  await page.goto(`/#/new?in=${idOf(portal)}`);
  const types = page.getByLabel("Type", { exact: true }).locator("option");
  await expect(types).toHaveText(["(choose a Type)", "incident"]);
  await page.getByLabel("Type", { exact: true }).selectOption("incident");
  await expect(page.getByLabel("f_assignee")).toHaveCount(0);
  await expect(page.getByLabel("f_due_resolution")).toHaveCount(0);
  await page.getByLabel("f_title").fill(`printer ${run}`);
  await page.getByLabel("f_priority").selectOption("P3");
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByRole("heading", { name: `printer ${run}` })).toBeVisible();
  const incidentId = page.url().split("/").pop()!;
  const incident = `${NS}/items/${incidentId}`;

  // Staff answer, and write a note.
  const sam = api(baseURL!, "sam");
  await sam.createItem({ parent: NS, requestId: randomUUID(), type: type("reply", 2), container: incident, fields: { f_body: `on it ${run}` } });
  await sam.createItem({ parent: NS, requestId: randomUUID(), type: type("note", 2), container: incident, fields: { f_body: `internal ${run}` } });
  await sam.updateItem({ name: incident, requestId: randomUUID(), fields: { f_assignee: "user:sam" } });

  await page.reload();
  const contained = page.getByRole("list", { name: "Contained Items" }).getByRole("listitem");
  await expect(contained).toHaveCount(1);
  await expect(contained).toContainText("reply@2");
  const fields = page.getByRole("region", { name: "Fields" });
  await expect(fields).not.toContainText("f_assignee");
  await expect(fields).not.toContainText("f_due_");
  const history = page.getByRole("list", { name: "History" }).getByRole("listitem");
  await expect(history.filter({ hasText: "sla_" })).toHaveCount(0);

  // Her moves: cancel (allowed names creator); not starting work.
  await expect(page.getByRole("button", { name: "Cancel" })).toBeEnabled();
  await expect(page.getByRole("button", { name: "Start" })).toHaveCount(0);

  // lee's incident does not exist for her.
  await page.goto(`/#/items/${idOf(leesIncident)}`);
  await expect(page.getByRole("alert")).toContainText("NOT_FOUND");

  // sam sees it all: the goals, the note, the reply.
  await actAs(page, "sam");
  await page.goto(`/#/items/${incidentId}`);
  const all = page.getByRole("list", { name: "Contained Items" }).getByRole("listitem");
  await expect(all.filter({ hasText: "sla_goal@3" })).toHaveCount(2);
  await expect(all.filter({ hasText: "note@2" })).toHaveCount(1);
  await expect(page.getByRole("region", { name: "Fields" })).toContainText("f_assignee");
});
