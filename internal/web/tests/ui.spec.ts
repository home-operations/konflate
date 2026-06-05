import { test, expect, type Page } from '@playwright/test';
import { samplePRs, diffEnvelope } from './fixtures';
import type { Meta } from '../src/lib/types';

const defaultMeta: Meta = { forge: 'github', repo: 'acme/home-ops', refreshIntervalSeconds: 1800 };

// Stub the konflate API (and silence the websocket) so the UI renders
// deterministically with no backend.
async function stubApi(page: Page, meta: Meta = defaultMeta) {
  await page.route('**/api/meta', (route) => route.fulfill({ json: meta }));
  await page.route('**/api/prs', (route) => route.fulfill({ json: samplePRs }));
  await page.route('**/api/prs/142/diff', (route) => route.fulfill({ json: diffEnvelope }));
  await page.routeWebSocket('**/ws', () => {
    /* accept; no live events needed */
  });
}

test('list → review → diffs flow', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');

  // Topbar shows the forge logo + repo, a GitHub link, and the auto-update
  // indicator — there is no manual refresh button.
  await expect(page.locator('.repo')).toContainText('acme/home-ops');
  await expect(page.locator('.repo svg[role="img"]')).toBeVisible();
  await expect(page.locator('a.gh')).toHaveAttribute('href', /github\.com\/home-operations\/konflate/);
  await expect(page.locator('.actions .auto')).toContainText('30m');
  await expect(page.locator('.actions .btn', { hasText: 'Refresh' })).toHaveCount(0);

  // Landing list: cards with per-PR signal badges.
  await expect(page.locator('.card')).toHaveCount(3);
  const card142 = page.locator('.card', { hasText: '#142' });
  await expect(card142.locator('.badge.danger').first()).toBeVisible();
  await expect(card142.locator('.ago')).toHaveText(/ago|just now/); // humanized last-refresh

  // Open a PR → summary-first Overview (URL deep-links).
  await card142.click();
  await expect(page).toHaveURL(/#\/pr\/142$/);
  await expect(page.locator('.danger-strip')).toContainText('danger');
  await expect(page.locator('.impact')).toContainText('resources');
  await expect(page.locator('.warning.danger')).toContainText('StatefulSet');
  await expect(page.locator('.img-list')).toContainText('ghcr.io/rook/ceph');
  await expect(page.locator('.failure')).toContainText('plex');
  await expect(page.locator('.ov-res')).toHaveCount(3);

  // Into the Diffs tab → tree rail + wide diff.
  await page.getByRole('button', { name: /^Diffs/ }).click();
  await expect(page).toHaveURL(/#\/pr\/142\/diffs/);
  await expect(page.locator('.tree .tree-item')).toHaveCount(3);
  await expect(page.locator('table.diff tr.row-add')).toBeVisible();
  await expect(page.locator('table.diff tr.row-del')).toBeVisible();

  // Mark viewed updates the progress counter.
  await page.locator('.viewed-btn').click();
  await expect(page.locator('.progress')).toContainText('1/3 viewed');
});

test('landing health summary + non-default base branch tag', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');

  // One-line health summary over the open set: 3 open, #142 danger, #131 failed
  // to render, #138 still rendering.
  const summary = page.locator('.list-summary');
  await expect(summary).toContainText('3 open');
  await expect(summary.locator('.sum-pill.danger')).toContainText(['1 danger', '1 failed']);
  await expect(summary).toContainText('1 rendering');
  await expect(summary.locator('.sum-pill.merged')).toContainText('1 merged');

  // Most PRs target main, so only #138 (→ staging) is flagged with a base tag.
  await expect(page.locator('.card', { hasText: '#138' }).locator('.base-tag')).toContainText('staging');
  await expect(page.locator('.card', { hasText: '#142' }).locator('.base-tag')).toHaveCount(0);
});

test('recently-merged PRs are grouped, collapsed, and marked frozen', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');

  // Landing: only the 3 open PRs render as cards; the merged one is collapsed.
  await expect(page.locator('.card')).toHaveCount(3);
  const group = page.locator('.group-head', { hasText: 'Recently merged' });
  await expect(group).toContainText('1');
  await expect(page.locator('.merged-cards')).toHaveCount(0);

  // Expand → the merged card appears, de-emphasized and labelled.
  await group.click();
  const mergedCard = page.locator('.merged-cards .card.merged', { hasText: '#128' });
  await expect(mergedCard).toBeVisible();
  await expect(mergedCard.locator('.merged-tag')).toContainText('merged');
  await expect(mergedCard.locator('.ago')).toContainText('merged');

  // Opening a merged PR shows the frozen-diff banner.
  await page.route('**/api/prs/128/diff', (route) =>
    route.fulfill({ json: { status: 'ready', pr: samplePRs[3], diff: { ...diffEnvelope.diff, prNumber: 128 } } }),
  );
  await mergedCard.click();
  await expect(page).toHaveURL(/#\/pr\/128$/);
  await expect(page.locator('.merged-strip')).toContainText('frozen');
});

test('split diff renders two balanced columns (regression)', async ({ page }) => {
  await stubApi(page);
  await page.goto('/#/pr/142/diffs/r0');
  await page.getByRole('button', { name: 'Split' }).click();
  await expect(page.locator('table.diff.split')).toBeVisible();
  // Both code columns must have real width — the bug collapsed the right cell to
  // ~0 (one character per line) because both claimed width:100%.
  const codeCells = page.locator('table.diff.split td.code');
  const left = await codeCells.nth(0).boundingBox();
  const right = await codeCells.nth(1).boundingBox();
  expect(left?.width ?? 0).toBeGreaterThan(200);
  expect(right?.width ?? 0).toBeGreaterThan(200);
});

test('unified view: word-level highlight + expandable folded context', async ({ page }) => {
  await stubApi(page);
  await page.goto('/#/pr/142/diffs/r0');
  await page.getByRole('button', { name: 'Unified' }).click();

  // Word-level highlight: only the changed part of the line is wrapped in .wd.
  await expect(page.locator('table.diff.unified tr.row-add .wd')).toHaveText('15.0');
  await expect(page.locator('table.diff.unified tr.row-del .wd')).toHaveText('14.9');

  // Folded context is hidden until its expander is clicked.
  const folded = page.locator('table.diff.unified tr.folded');
  await expect(folded).toHaveCount(0);
  const expander = page.locator('.expand-btn', { hasText: 'Expand 2 unchanged lines' });
  await expect(expander).toBeVisible();
  await expander.click();
  await expect(folded).toHaveCount(2);
  await expect(folded.first()).toContainText('metadata');
  // Clicking again collapses.
  await page.locator('.expand-btn', { hasText: 'Collapse' }).click();
  await expect(folded).toHaveCount(0);
});

test('list shows a spinner for rendering PRs and a queued icon for pending', async ({ page }) => {
  const pr = (number: number, status: string, title: string) => ({
    number, title, author: 'x', state: 'open', open: true, draft: false,
    headRef: 'h', headSha: 'h', baseRef: 'main', labels: [], url: '', status,
    updatedAt: '2026-06-04T12:00:00Z',
  });
  await page.route('**/api/meta', (r) => r.fulfill({ json: defaultMeta }));
  await page.route('**/api/prs', (r) => r.fulfill({ json: [pr(1, 'running', 'busy pr'), pr(2, 'pending', 'waiting pr')] }));
  await page.routeWebSocket('**/ws', () => {});
  await page.goto('/');

  const running = page.locator('.card', { hasText: 'busy pr' });
  await expect(running.locator('.card-status.running .kspin')).toBeVisible(); // thematic wheel spinner
  await expect(running).toContainText('rendering');
  await expect(page.locator('.card', { hasText: 'waiting pr' })).toContainText('queued');
});

test('review body shows a big spinner while a diff is still rendering', async ({ page }) => {
  await page.route('**/api/meta', (r) => r.fulfill({ json: defaultMeta }));
  await page.route('**/api/prs', (r) =>
    r.fulfill({
      json: [{
        number: 5, title: 'rendering', author: 'x', state: 'open', open: true, draft: false,
        headRef: 'h', headSha: 'h', baseRef: 'main', labels: [], url: '', status: 'running',
        updatedAt: '2026-06-04T12:00:00Z',
      }],
    }),
  );
  await page.route('**/api/prs/5/diff', (r) => r.fulfill({ status: 202, json: { status: 'running', pr: { number: 5 } } }));
  await page.routeWebSocket('**/ws', () => {});
  await page.goto('/#/pr/5');
  await expect(page.locator('.loading-center .kspin')).toBeVisible();
  await expect(page.locator('.loading-center')).toContainText('Rendering');
});

test('deep link opens a specific resource diff', async ({ page }) => {
  await stubApi(page);
  await page.goto('/#/pr/142/diffs/r0');
  await expect(page.locator('.res-title')).toContainText('rook-ceph-operator');
  await expect(page.locator('table.diff tr.row-add')).toBeVisible();
});

test('keyboard: j selects the next resource', async ({ page }) => {
  await stubApi(page);
  await page.goto('/#/pr/142');
  // Resource navigation needs the diff loaded; wait for the overview to render.
  await page.locator('.impact').waitFor();
  await page.locator('body').press('j');
  await expect(page).toHaveURL(/#\/pr\/142\/diffs\/r0/);
  await page.locator('body').press('j');
  await expect(page).toHaveURL(/#\/pr\/142\/diffs\/r1/);
});

test('auto-update indicator reflects the configured interval, no refresh button', async ({ page }) => {
  await stubApi(page, { forge: 'forgejo', repo: 'me/home-ops', refreshIntervalSeconds: 600 });
  await page.goto('/');
  await expect(page.locator('.actions .auto')).toContainText('10m');
  await expect(page.locator('.actions .btn', { hasText: 'Refresh' })).toHaveCount(0);
  await expect(page.locator('a.gh')).toBeVisible();
  await page.screenshot({ path: 'screenshots/konflate-auto.png' });
});

test('filter narrows the PR list', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');
  await page.getByPlaceholder('Filter pull requests…').fill('plex');
  await expect(page.locator('.card')).toHaveCount(1);
  await expect(page.locator('.card')).toContainText('#131');
});

test('image changes shorten digest versions (full value on hover + correct copy)', async ({ page }) => {
  await page.addInitScript(() => {
    (window as unknown as { __copied: string[] }).__copied = [];
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: {
        writeText: (t: string) => {
          (window as unknown as { __copied: string[] }).__copied.push(t);
          return Promise.resolve();
        },
      },
    });
  });
  await stubApi(page);
  await page.goto('/#/pr/142');

  const block = page.locator('.img-change', { hasText: 'thelounge' });
  // Displayed as sha256:<12 hex>…, never the full 64-hex digest (which blew out the width).
  await expect(block.locator('.img-ver.to')).toHaveText('sha256:7f2fff6e2644…');
  await expect(block.locator('.img-ver.from')).toHaveText('sha256:9c3667236b1a…');
  await expect(block.locator('.img-ver.to')).not.toContainText('aadf18cd1abc'); // the truncated tail
  // Full digest preserved on hover.
  await expect(block.locator('.img-ver.to')).toHaveAttribute(
    'title',
    'sha256:7f2fff6e264411ce8608bd1fdf5142a3cd980677b0479e7e3702aadf18cd1abc',
  );
  // Copy reconstructs a digest reference with '@' (not a malformed second ':').
  await block.locator('.copy-btn').click();
  const copied = await page.evaluate(() => (window as unknown as { __copied: string[] }).__copied);
  expect(copied).toContain(
    'ghcr.io/thelounge/thelounge:4.5.0@sha256:7f2fff6e264411ce8608bd1fdf5142a3cd980677b0479e7e3702aadf18cd1abc',
  );
});

test('mobile: diff header title is not crushed to one char per line (regression)', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await stubApi(page);
  await page.goto('/#/pr/142/diffs/r0');
  const title = page.locator('.res-title');
  await title.waitFor();
  const box = await title.boundingBox();
  // The bug squeezed the title into a ~0-width flex column, so break-all stacked
  // it one character per line (~28 lines tall). Fixed, it's a readable 1–2 rows.
  expect(box?.height ?? 999).toBeLessThan(120);
  expect(box?.width ?? 0).toBeGreaterThan(150);
});

test('copy buttons copy the full underlying value', async ({ page }) => {
  // Record clipboard writes deterministically (no OS clipboard / permissions).
  await page.addInitScript(() => {
    (window as unknown as { __copied: string[] }).__copied = [];
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: {
        writeText: (t: string) => {
          (window as unknown as { __copied: string[] }).__copied.push(t);
          return Promise.resolve();
        },
      },
    });
  });
  await stubApi(page);
  await page.goto('/#/pr/142');

  // Head SHA copies the full SHA, not the 7-char display.
  await page.locator('.sha-wrap .copy-btn').click();
  // Image-change row copies the full name:tag reference.
  await page.locator('.img-change .copy-btn').first().click();
  const copied = await page.evaluate(() => (window as unknown as { __copied: string[] }).__copied);
  expect(copied).toContain('a1b2c3d4e5f6');
  expect(copied).toContain('ghcr.io/rook/ceph:v1.15.0');

  // Resource identifier copies from the diff header.
  await page.goto('/#/pr/142/diffs/r0');
  await page.locator('.res-header .copy-btn').click();
  const copied2 = await page.evaluate(() => (window as unknown as { __copied: string[] }).__copied);
  expect(copied2).toContain('Deployment rook-ceph/rook-ceph-operator');
});

test('captures list + overview screenshots (light)', async ({ page }) => {
  await stubApi(page);
  await page.addInitScript(() => localStorage.setItem('konflate-theme', 'light'));
  await page.goto('/');
  await expect(page.locator('.card')).toHaveCount(3);
  await page.screenshot({ path: 'screenshots/konflate-list.png' });

  await page.locator('.card', { hasText: '#142' }).click();
  await expect(page.locator('.impact')).toBeVisible();
  await page.screenshot({ path: 'screenshots/konflate-overview.png' });
});

test('captures diffs screenshot (dark, deep-linked)', async ({ page }) => {
  await stubApi(page);
  await page.addInitScript(() => localStorage.setItem('konflate-theme', 'dark'));
  await page.goto('/#/pr/142/diffs/r0');
  await expect(page.locator('table.diff tr.row-add')).toBeVisible();
  await expect(page.locator('html.dark')).toBeAttached();
  await page.screenshot({ path: 'screenshots/konflate-diffs-dark.png' });
});
