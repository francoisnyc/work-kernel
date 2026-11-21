// kim opens a request in the customer portal, and never sees a note, a
// goal, a due instant or the assignee.
//
//   - she signs in and gets the portal only; she picks a desk and opens a
//     request (title, urgency, description); it shows as Received, and in
//     My requests;
//   - staff work it through the API: a note, a reply, an assignee (bob,
//     who created nothing kim sees: an Item's creator is part of what a
//     customer sees, and lea created the desk), waiting for the customer;
//   - she sees the reply from "sam · Support" and "Waiting for you", never
//     the note; her reply brings the request back to In progress;
//   - every API response she received is scanned: no note or sla_goal Item,
//     no assignee or due-instant field, none of their values;
//   - axe on the portal's screens.
//
// Run against the stack: docker compose up -d --build --wait (or make
// scenario), then `npm run e2e` in apps/poc.
import { expect, test, type Response } from "@playwright/test";
import { NS, api, createItem, dueInstants, seedDesk, transition, type, update, type Item } from "./seed";
import { expectAccessible, refreshUntil, signIn } from "./support";

const run = `p${Date.now().toString(36)}`;
const deskName = `E2E portal ${run}`;
const title = `Printer jam ${run}`;
const description = `It eats paper ${run}.`;
const note = `secret-note-${run}`;
const reply = `public-reply-${run}`;

// The fields a customer never receives, under any key path.
const hiddenFields = ["f_assignee", "f_due_first_response", "f_due_resolution", "f_due_at", "f_breached_at", "f_outcome"];

// Type definitions are readable by anyone, customers included (design.md,
// "Jira part 2, step 4a": Types, as for anyone): they name the fields and
// the goal Type, and hold no Item data. They are left out of the key and
// Type checks, and still searched for values.
const typeCalls = /\/kernel\.v1\.ItemService\/(GetType|ListTypes)$/;

function walk(value: unknown, path: string, visit: (path: string, key: string, v: unknown) => void) {
  if (Array.isArray(value)) value.forEach((v, i) => walk(v, `${path}[${i}]`, visit));
  else if (value && typeof value === "object") {
    for (const [k, v] of Object.entries(value)) {
      visit(path, k, v);
      walk(v, `${path}.${k}`, visit);
    }
  }
}

test("kim opens a request and sees only the public conversation", async ({ page, baseURL }) => {
  await seedDesk(baseURL!, run, deskName);

  // Every response from the API, kept to scan at the end.
  const bodies: Promise<{ url: string; text: string }>[] = [];
  page.on("response", (res: Response) => {
    if (new URL(res.url()).pathname.startsWith("/api/")) bodies.push(res.text().then((text) => ({ url: res.url(), text })));
  });

  await signIn(page, "kim");
  await expect(page).toHaveURL(/#\/portal/);
  await expect(page.getByRole("heading", { level: 1, name: "How can we help?" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Service desk" })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "Tracker" })).toHaveCount(0);
  await expectAccessible(page, "the portal's home");

  // A new request.
  await page.getByRole("link", { name: deskName }).click();
  await expect(page.getByRole("heading", { level: 1, name: "New request" })).toBeVisible();
  await expect(page.getByLabel("How urgent is it?")).toHaveValue("P3");
  await page.getByRole("button", { name: "Send request" }).click();
  await expect(page.getByText("Give your request a title.")).toBeVisible();
  await page.getByRole("textbox", { name: "Title" }).fill(title);
  await page.getByLabel("How urgent is it?").selectOption("P2");
  await page.getByRole("textbox", { name: "Describe the problem" }).fill(description);
  await expectAccessible(page, "the new request form");
  await page.getByRole("button", { name: "Send request" }).click();

  // Her request.
  await expect(page.getByRole("heading", { level: 1, name: title })).toBeVisible();
  await expect(page.getByLabel("Status")).toHaveText("Received");
  await expect(page.getByRole("article", { name: /^Reply by You/ })).toContainText(description);
  await expect(page.getByRole("button", { name: "Cancel request" })).toBeVisible();

  // My requests.
  await page.getByRole("link", { name: "My requests" }).click();
  await refreshUntil(page, async () => {
    await expect(page.getByRole("row").filter({ hasText: title })).toContainText("Received");
  });
  await expectAccessible(page, "my requests");

  // Staff work it, through the API.
  const sam = api(baseURL!, "sam");
  const found = (await sam("SearchItems", { parent: NS, query: title, filter: { types: [`${NS}/types/incident`] } })).items as Item[];
  expect(found).toHaveLength(1);
  const incident = await dueInstants(baseURL!, found[0]!);
  const dues = [String(incident.fields!.f_due_first_response), String(incident.fields!.f_due_resolution)];
  await transition(sam, incident.name, "in_progress");
  await createItem(sam, type("note", 1), { f_body: note }, incident.name);
  await createItem(sam, type("reply", 1), { f_body: reply }, incident.name);
  await update(sam, incident.name, { f_assignee: "user:bob" });
  await transition(sam, incident.name, "waiting_for_customer");

  // What kim sees of it.
  await page.getByRole("row").filter({ hasText: title }).getByRole("link", { name: title }).click();
  await refreshUntil(page, async () => {
    await expect(page.getByLabel("Status")).toHaveText("Waiting for you");
  });
  await expect(page.getByText("We need your reply")).toBeVisible();
  await expect(page.getByRole("article", { name: /^Reply by sam · Support/ })).toContainText(reply);
  const body = page.locator("body");
  await expect(body).not.toContainText(note);
  for (const word of [/\bInternal\b/i, /\bFirst response\b/i, /\bResolution due\b/i, /\bBreached\b/i, /\bwall clock\b/i, /\bbob\b/]) {
    await expect(body).not.toContainText(word);
  }
  await expectAccessible(page, "her request");

  // Her reply brings it back to staff.
  const answer = `more details ${run}`;
  await page.getByRole("textbox", { name: "Your reply" }).fill(answer);
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await expect(page.getByRole("article", { name: /^Reply by You/ }).filter({ hasText: answer })).toBeVisible();
  await refreshUntil(page, async () => {
    await expect(page.getByLabel("Status")).toHaveText("In progress");
  });

  // Every response she received.
  const all = await Promise.all(bodies);
  expect(all.some((b) => b.text.includes(reply)), "the scan saw the reply").toBe(true);
  for (const { url, text } of all) {
    // Exact values: a JSON string, quotes included (the note is free text).
    for (const secret of [note, ...[...dues, "user:bob"].map((v) => JSON.stringify(v))]) {
      expect(text.includes(secret), `${url} holds ${secret}`).toBe(false);
    }
    if (typeCalls.test(new URL(url).pathname) || text === "") continue;
    walk(JSON.parse(text), "$", (path, key, v) => {
      expect(hiddenFields.includes(key), `${url} ${path} has ${key}`).toBe(false);
      if (key === "type" && typeof v === "string") {
        expect(v, `${url} ${path}.type`).not.toMatch(/\/types\/(note|sla_goal)\//);
      }
    });
  }
});
