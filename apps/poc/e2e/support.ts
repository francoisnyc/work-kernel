// What the journeys share: signing in, waiting for the server through the
// Refresh button, table cells by their header, drags, and axe.
import AxeBuilder from "@axe-core/playwright";
import { expect, type Locator, type Page } from "@playwright/test";

// Signs in on the dev sign-in screen and waits for the person menu.
export async function signIn(page: Page, name: string): Promise<void> {
  await page.goto("/#/signin");
  await page.getByRole("button", { name: `Sign in as ${name}` }).click();
  await expect(page.getByRole("button", { name: `Signed in as ${name}` })).toBeVisible();
}

// There is no Watch: what the worker does lands on the next read. Clicks
// Refresh until the assertion holds.
export async function refreshUntil(page: Page, assertion: () => Promise<void>, timeout = 10_000): Promise<void> {
  await expect(async () => {
    await page.getByRole("button", { name: "Refresh" }).click();
    await assertion();
  }).toPass({ timeout, intervals: [250, 500, 1000] });
}

// A row's cell under a column header (headers may carry a caveat:
// "Resolution due (wall clock)").
export async function cellOf(table: Locator, row: Locator, header: RegExp): Promise<Locator> {
  const headers = await table.getByRole("columnheader").allTextContents();
  const i = headers.findIndex((h) => header.test(h));
  if (i < 0) throw new Error(`no column ${header} among ${headers.join(" | ")}`);
  return row.getByRole("cell").nth(i);
}

// A pointer drag from one element to a point on another: its top edge
// ("before" it), its bottom edge ("after" it) or its middle.
export async function drag(page: Page, from: Locator, to: Locator, where: "top" | "bottom" | "middle"): Promise<void> {
  const a = (await from.boundingBox())!;
  const b = (await to.boundingBox())!;
  const y = where === "top" ? b.y + 4 : where === "bottom" ? b.y + b.height - 4 : b.y + b.height / 2;
  await page.mouse.move(a.x + a.width / 2, a.y + a.height / 2);
  await page.mouse.down();
  await page.mouse.move(a.x + a.width / 2 + 10, a.y + a.height / 2 + 10, { steps: 3 });
  await page.mouse.move(b.x + b.width / 2, y, { steps: 10 });
  await page.mouse.up();
}

// WCAG 2.1 A and AA, as axe checks them, on the page as it is now.
export async function expectAccessible(page: Page, screen: string): Promise<void> {
  const result = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
  const found = result.violations.map((v) => `${v.id} (${v.impact}): ${v.help} — ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`);
  expect(found, `axe on ${screen}`).toEqual([]);
}
