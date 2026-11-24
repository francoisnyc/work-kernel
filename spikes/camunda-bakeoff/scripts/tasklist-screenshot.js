// Screenshot of Camunda Tasklist as fay (test 3's evidence). Needs the
// Camunda side up and Playwright (preinstalled in the cloud session):
//   node scripts/tasklist-screenshot.js
const { chromium } = require('playwright');
(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1400, height: 900 }, deviceScaleFactor: 1 });
  await page.goto('http://127.0.0.1:8088/tasklist');
  await page.fill('input[name="username"], #username', 'fay');
  await page.fill('input[name="password"], #password', 'fay-pw');
  await page.click('button[type="submit"]');
  await page.waitForTimeout(5000);
  await page.screenshot({ path: __dirname + '/../results/tasklist-fay.png' });
  const items = page.locator('[data-testid="task-panel"] a, [data-testid^="task-"]');
  if (await items.count() > 0) { await items.first().click(); await page.waitForTimeout(3000);
    await page.screenshot({ path: __dirname + '/../results/tasklist-fay-task.png' }); }
  await browser.close();
})();
