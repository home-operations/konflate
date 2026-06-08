import { test, expect, type Page } from '@playwright/test';
import type { Meta, PRStatus } from '../src/lib/types';

const meta: Meta = { forge: 'github', repo: 'acme/home-ops', refreshIntervalSeconds: 1800 };

// A self-contained PR set covering the triage axes: a caution-free bump, a PR
// carrying cautions, and one still rendering. Kept separate from the shared
// fixture so the existing card-count assertions there stay valid.
function pr(over: Partial<PRStatus> & { number: number; title: string }): PRStatus {
  return {
    author: 'renovate[bot]',
    state: 'open',
    open: true,
    draft: false,
    headRef: `pr/${over.number}`,
    headSha: 'abc',
    baseRef: 'main',
    createdAt: '2026-06-01T09:00:00Z',
    updatedAt: '2026-06-04T12:00:00Z',
    labels: [],
    url: `https://github.com/acme/home-ops/pull/${over.number}`,
    status: 'ready',
    ...over,
  };
}

const prs: PRStatus[] = [
  pr({
    number: 1,
    title: 'clean image bump',
    signals: { resources: 2, caution: 0, images: 1, failures: 0 },
  }),
  pr({
    number: 2,
    title: 'risky rbac change',
    signals: { resources: 5, caution: 2, images: 0, failures: 0 },
  }),
  pr({ number: 3, title: 'still rendering', status: 'running' }),
];

async function stubApi(page: Page) {
  await page.route('**/api/meta', (r) => r.fulfill({ json: meta }));
  await page.route('**/api/prs', (r) => r.fulfill({ json: prs }));
  await page.routeWebSocket('**/ws', () => {
    /* accept */
  });
}

const cards = (page: Page) => page.locator('.cards .card');

test('the caution pill narrows to PRs carrying cautions', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');

  // All three open PRs are listed to start.
  await expect(cards(page)).toHaveCount(3);

  // The caution pill (count 1) filters the list down to just the PR with cautions.
  const cautionPill = page.locator('button.sum-pill', { hasText: 'caution' });
  await expect(cautionPill).toContainText('1');
  await cautionPill.click();
  await expect(cards(page)).toHaveCount(1);
  await expect(cards(page).first()).toContainText('risky rbac change');
});

test('status:caution query facet matches the pill filter', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');

  await page.locator('input.pr-search').fill('status:caution');
  await expect(cards(page)).toHaveCount(1);
  await expect(cards(page).first()).toContainText('risky rbac change');
});
