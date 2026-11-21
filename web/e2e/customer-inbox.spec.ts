// The fix of the 2026-11-21 review, in the browser: kim's Inbox runs no
// search on a field she cannot see. It shows no refusal, and the page makes
// no SearchItems call on a hidden field.
//
// Run: docker compose up -d --build --wait, then `npm run e2e` in web/.
import { expect, test } from "@playwright/test";

test("kim's Inbox: no refusal, no search on a field she cannot see", async ({ page }) => {
  const searches: string[] = [];
  page.on("request", (req) => {
    if (req.url().endsWith("/SearchItems")) searches.push(req.postData() ?? "");
  });
  await page.goto("/");
  await page.getByLabel("Act as").selectOption("kim");
  await expect(page.getByRole("banner")).toContainText("acting as kim");
  await page.getByRole("link", { name: "Inbox" }).click();
  await expect(page.getByRole("heading", { name: "Inbox" })).toBeVisible();
  await expect(page.getByRole("alert")).toHaveCount(0);
  for (const body of searches) {
    for (const hidden of ["f_assignee", "f_approver", "f_reviewers", "f_owner"]) expect(body).not.toContain(hidden);
  }
});
