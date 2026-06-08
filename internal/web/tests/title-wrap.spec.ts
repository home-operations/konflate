import { test, expect, type Page } from '@playwright/test';
import type { Meta, PRStatus } from '../src/lib/types';

const meta: Meta = { forge: 'github', repo: 'acme/home-ops', refreshIntervalSeconds: 1800 };

// A Renovate-style title with a slash-heavy image path (3 slashes).
const title = 'feat(container): update image ghcr.io/external-secrets/charts/external-secrets (2.5.0 → 2.6.0)';

const prs: PRStatus[] = [
  {
    number: 1, title, author: 'bot-ross[bot]', state: 'open', open: true, draft: false,
    headRef: 'h', headSha: 'h', baseRef: 'main', createdAt: '2026-06-01T09:00:00Z',
    updatedAt: '2026-06-04T12:00:00Z', labels: [], url: '#', status: 'ready',
    signals: { resources: 4, danger: 0, caution: 0, images: 1, failures: 0 },
  },
];

async function stub(page: Page) {
  await page.route('**/api/meta', (r) => r.fulfill({ json: meta }));
  await page.route('**/api/prs', (r) => r.fulfill({ json: prs }));
  await page.routeWebSocket('**/ws', () => {});
}

test('long slash titles wrap at each slash (a <wbr> after every "/")', async ({ page }) => {
  await stub(page);
  await page.goto('/');

  const cardTitle = page.locator('.card-title');
  // One break opportunity after each of the 3 slashes — so the slash ends the
  // line, like the browser already does for "-".
  await expect(cardTitle.locator('wbr')).toHaveCount(3);
  // <wbr> adds no text: the visible title (and copy/title attr) is unchanged.
  await expect(cardTitle).toHaveText(title);
});
