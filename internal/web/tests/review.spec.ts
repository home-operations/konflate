import { test, type Page } from '@playwright/test';
import { samplePRs, diffEnvelope } from './fixtures';
import type { Meta } from '../src/lib/types';

// Screenshot sweep for manual UI review at mobile + desktop widths. Not an
// assertion test — it captures the key screens so layout inconsistencies are
// easy to eyeball side by side. Output lands in screenshots/.

const meta: Meta = { forge: 'github', repo: 'acme/home-ops', refreshIntervalSeconds: 1800 };

async function stub(page: Page): Promise<void> {
  await page.route('**/api/meta', (r) => r.fulfill({ json: meta }));
  await page.route('**/api/prs', (r) => r.fulfill({ json: samplePRs }));
  await page.route('**/api/prs/142/diff', (r) => r.fulfill({ json: diffEnvelope }));
  await page.routeWebSocket('**/ws', () => {});
}

const viewports = [
  { tag: 'desktop', width: 1440, height: 900 },
  { tag: 'mobile', width: 390, height: 844 },
];

for (const vp of viewports) {
  test.describe(`${vp.tag} (${vp.width}px)`, () => {
    test.use({ viewport: { width: vp.width, height: vp.height } });

    test('list', async ({ page }) => {
      await stub(page);
      await page.goto('/');
      await page.locator('.card').first().waitFor();
      await page.screenshot({ path: `screenshots/${vp.tag}-list.png`, fullPage: true });
      // Expand the "recently merged" shelf for the grouped view.
      await page.locator('.group-head').click();
      await page.locator('.merged-cards .card').first().waitFor();
      await page.screenshot({ path: `screenshots/${vp.tag}-list-merged.png`, fullPage: true });
    });

    test('overview', async ({ page }) => {
      await stub(page);
      await page.goto('/#/pr/142');
      await page.locator('.impact').waitFor();
      await page.screenshot({ path: `screenshots/${vp.tag}-overview.png`, fullPage: true });
    });

    test('diffs-unified', async ({ page }) => {
      await stub(page);
      await page.goto('/#/pr/142/r0');
      // The toggle only exists where split is offered; phones are unified-only,
      // so click it when present, otherwise we're already unified.
      const unified = page.getByRole('button', { name: 'Unified' });
      if (await unified.count()) await unified.click();
      await page.locator('table.diff.unified').waitFor();
      await page.screenshot({ path: `screenshots/${vp.tag}-diffs-unified.png`, fullPage: true });
    });

    test('diffs-split', async ({ page }) => {
      test.skip(vp.tag === 'mobile', 'split view is unavailable at phone width (unified only)');
      await stub(page);
      await page.goto('/#/pr/142/r0');
      await page.getByRole('button', { name: 'Split' }).click();
      await page.locator('table.diff.split').waitFor();
      await page.screenshot({ path: `screenshots/${vp.tag}-diffs-split.png`, fullPage: true });
    });
  });
}
