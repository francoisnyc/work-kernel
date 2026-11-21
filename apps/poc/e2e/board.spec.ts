// lea runs a sprint in the tracker.
//
//   - the board: columns by state in rank order; a pointer drag ranks a
//     card (kept after a reload); the keyboard moves a card to another
//     column (a transition, then its rank); a drag to Done asks for the
//     resolution; a drop the Type does not declare is refused before
//     anything is written;
//   - an issue: its key, priority edited inline, a link added, a comment,
//     the history in sentences;
//   - the backlog: the sprint's section, a quick-created issue in the
//     backlog, a word search held to the space;
//   - epics: progress from the rollup, and the server's refusal of "Mark
//     done" in plain words;
//   - axe on each screen.
//
// Run against the stack: docker compose up -d --build --wait (or make
// scenario), then `npm run e2e` in apps/poc.
import { expect, test, type Page } from "@playwright/test";
import { seedProject } from "./seed";
import { drag, expectAccessible, signIn } from "./support";

const run = `b${Date.now().toString(36)}`;
const A = `Card A ${run}`;
const B = `Card B ${run}`;
const C = `Card C ${run}`;
const D = `Card D ${run}`;

const column = (page: Page, name: string) => page.getByRole("list", { name });
const cardIn = (page: Page, col: string, title: string) => column(page, col).getByRole("listitem").filter({ hasText: title });

test("lea runs a sprint", async ({ page, baseURL }) => {
  await seedProject(baseURL!, run);

  await signIn(page, "lea");
  await page.getByRole("link", { name: "Tracker" }).click();
  await page.getByRole("link", { name: `E2E space ${run}` }).click();
  await page.getByRole("link", { name: "Board" }).click();

  // The board.
  await expect(page.getByRole("heading", { level: 1, name: `Sprint ${run}` })).toBeVisible();
  await expect(page.getByText("Ship sign-on")).toBeVisible();
  await expect(page.getByText(/Ends in 13 d \d+ h \(wall clock\)/)).toBeVisible();
  await expect(column(page, "To do").getByRole("link")).toHaveText([A, B, C]);
  await expect(column(page, "In progress").getByRole("link")).toHaveText([D]);
  await expect(column(page, "In review").getByRole("link")).toHaveCount(0);
  await expect(column(page, "Done").getByRole("link")).toHaveCount(0);
  await expectAccessible(page, "the board");

  // A pointer drag ranks C first; a reload keeps it.
  await drag(page, cardIn(page, "To do", C), cardIn(page, "To do", A), "top");
  await expect(column(page, "To do").getByRole("link")).toHaveText([C, A, B]);
  await page.reload();
  await expect(column(page, "To do").getByRole("link")).toHaveText([C, A, B]);

  // The keyboard: B picked up, moved right, dropped: In progress, after D.
  const announcer = page.locator('[aria-live="assertive"]');
  await page.getByRole("button", { name: `Move ${B}` }).focus();
  await page.keyboard.press("Space");
  await expect(announcer).toContainText(`Picked up ${B}`);
  await page.keyboard.press("ArrowRight");
  await expect(announcer).toContainText("In progress");
  await page.keyboard.press("Space");
  await expect(column(page, "In progress").getByRole("link")).toHaveText([D, B]);
  await expect(column(page, "To do").getByRole("link")).toHaveText([C, A]);
  await page.reload();
  await expect(column(page, "In progress").getByRole("link")).toHaveText([D, B]);

  // A drag to Done asks for the resolution first.
  await drag(page, cardIn(page, "In progress", D), page.getByRole("region", { name: "Done" }), "middle");
  const dialog = page.getByRole("dialog", { name: "Mark done" });
  await dialog.getByRole("combobox", { name: "Resolution" }).selectOption("fixed");
  await dialog.getByRole("button", { name: "Mark done" }).click();
  await expect(column(page, "Done").getByRole("link")).toHaveText([D]);

  // A move the Type does not declare: refused, nothing written.
  await drag(page, cardIn(page, "To do", A), page.getByRole("region", { name: "Done" }), "middle");
  await expect(page.getByRole("alert")).toContainText("can't go from To do to Done");
  await expect(column(page, "To do").getByRole("link")).toHaveText([C, A]);
  await expect(page.getByRole("dialog")).toHaveCount(0);

  // An issue.
  await column(page, "To do").getByRole("link", { name: C }).click();
  await expect(page.getByRole("heading", { level: 1, name: C })).toBeVisible();
  await expect(page.getByText(new RegExp(`^E${run} · [0-9a-f]{8}$`))).toBeVisible();
  await expect(page.getByLabel("Status")).toHaveText("To do");
  await expect(page.getByRole("button", { name: "Start progress" })).toBeVisible();
  await page.getByLabel("Priority").selectOption("P1");
  const history = page.getByRole("region", { name: "History" });
  await expect(history).toContainText("lea set priority to P1");
  // A link: C blocks A.
  await page.getByRole("button", { name: "Add link" }).click();
  const linkDialog = page.getByRole("dialog", { name: "Add link" });
  await linkDialog.getByRole("combobox", { name: "Link type" }).selectOption("blocks");
  await linkDialog.getByRole("combobox", { name: "Find an issue" }).fill(A);
  await linkDialog.getByRole("option", { name: new RegExp(A) }).click();
  await linkDialog.getByRole("button", { name: "Add link" }).click();
  const links = page.getByRole("region", { name: "Links" });
  await expect(links.getByRole("listitem").filter({ hasText: A })).toContainText("To do");
  await expect(links).toContainText("Blocks");
  await expect(history).toContainText(`lea linked this: blocks ${A}`);
  // A comment.
  const comment = `comment-${run}: let's split this`;
  await page.getByRole("textbox", { name: "Add a comment" }).fill(comment);
  await page.getByRole("button", { name: "Comment" }).click();
  await expect(page.getByRole("article", { name: /^Comment by You/ })).toContainText(comment);
  await expectAccessible(page, "an issue");

  // The backlog: the sprint's section, a quick-created issue, a search.
  await page.getByRole("link", { name: "Backlog" }).click();
  const sprintSection = page.getByRole("region", { name: `Sprint ${run}` });
  await expect(sprintSection.getByRole("link")).toHaveText([C, A, D, B]);
  const fresh = `Card E ${run}`;
  await page.getByRole("textbox", { name: "New issue summary" }).fill(fresh);
  await page.getByLabel("Issue type").selectOption("task");
  await page.getByRole("button", { name: "Create" }).click();
  const backlog = page.getByRole("region", { name: "Backlog" });
  await expect(backlog.getByRole("link", { name: fresh })).toBeVisible();
  await expect(backlog.getByRole("link", { name: A })).toHaveCount(0);
  await page.getByRole("searchbox", { name: "Search issues" }).fill(`Card E ${run}`);
  await page.keyboard.press("Enter");
  await expect(page.getByRole("main").getByRole("link", { name: /^Card [A-E] / })).toHaveText([fresh]);
  await expectAccessible(page, "the backlog");

  // Epics: progress over what lea sees, and the guard's refusal in words.
  await page.getByRole("link", { name: "Epics" }).click();
  const epicRow = page.getByRole("listitem").filter({ hasText: `Sign-on ${run}` });
  await expect(epicRow).toContainText("1 of 4 done");
  await expect(epicRow.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "25");
  await expectAccessible(page, "the epics");
  await epicRow.getByRole("link", { name: `Sign-on ${run}` }).click();
  await page.getByRole("button", { name: "Start" }).click();
  await expect(page.getByLabel("Status")).toHaveText("In progress");
  await page.getByRole("button", { name: "Mark done" }).click();
  await expect(page.getByRole("alert")).toContainText("An epic is done only when all its issues are done or canceled.");
  await expect(page.getByLabel("Status")).toHaveText("In progress");
  await expectAccessible(page, "an epic, refused");
});
