import { test, expect, type Page } from '@playwright/test';
import { samplePRs, sampleDiff } from './fixtures';
import type { DiffEnvelope, Meta, PRStatus } from '../src/lib/types';

const meta: Meta = { forge: 'github', repo: 'acme/home-ops', refreshIntervalSeconds: 1800 };

// A PR whose new image tag doesn't exist upstream (KONFLATE_VERIFY_IMAGES): the
// image-not-found blocker is attributed to the Deployment that pulls it, and the
// image list carries the registry verdict per image — missing, found, and one the
// registry never confirmed. Self-contained so the shared fixture's counts hold.
const pr: PRStatus = {
  ...samplePRs[0],
  number: 7,
  title: "bump rook to a tag that isn't published yet",
  checks: undefined,
  signals: { resources: 3, caution: 2, blocking: 1, images: 3, failures: 0, routine: false },
};
const clean: PRStatus = {
  ...samplePRs[0],
  number: 8,
  title: 'clean bump',
  checks: undefined,
  signals: { resources: 1, caution: 0, blocking: 0, images: 1, failures: 0, routine: true },
};

const envelope: DiffEnvelope = {
  status: 'ready',
  pr,
  diff: {
    ...sampleDiff,
    prNumber: 7,
    failures: [],
    images: [
      { ...sampleDiff.images[0], upstream: 'missing' },
      { ...sampleDiff.images[1], upstream: 'found' },
      { name: 'registry.example.com/private/app', from: '1.0', to: '1.1', refs: [] },
    ],
    // The blocker is appended after the cautions, as the server does, and the
    // Deployment it names also carries an ordinary caution (mixed tiers).
    warnings: [
      ...sampleDiff.warnings,
      {
        level: 'caution',
        rule: 'replicas-zero',
        resource: 'Deployment rook-ceph/rook-ceph-operator',
        detail: 'replicas set to 0 — the workload will be scaled to zero',
      },
      {
        level: 'blocking',
        rule: 'image-not-found',
        resource: 'Deployment rook-ceph/rook-ceph-operator',
        detail: 'image ghcr.io/rook/ceph:v1.15.0 not found in its registry — it would fail to pull',
      },
    ],
  },
};

async function stubApi(page: Page) {
  await page.route('**/api/meta', (r) => r.fulfill({ json: meta }));
  await page.route('**/api/prs', (r) => r.fulfill({ json: [pr, clean] }));
  await page.route('**/api/prs/7/diff', (r) => r.fulfill({ json: envelope }));
  await page.route('**/api/prs/7/summary', (r) => r.fulfill({ json: envelope }));
  await page.routeWebSocket('**/ws', () => {
    /* accept */
  });
}

test('a blocker gets its own red pill, card edge and badge, and a status:blocking facet', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');
  const cards = page.locator('.cards .card');
  await expect(cards).toHaveCount(2);

  const card7 = page.locator('.card-shell[data-pr="7"]');
  await expect(card7).toHaveClass(/blocking/);
  await expect(card7.locator('.badge.blocking')).toContainText('1');
  // The blocker's badge sits left of the amber caution badge: severity order.
  const classes = await card7.locator('.badges .badge').evaluateAll((els) => els.map((e) => e.className));
  expect(classes.findIndex((c) => c.includes('blocking'))).toBeLessThan(classes.findIndex((c) => c.includes('caution')));

  const pill = page.locator('.sum-pill.blocking');
  await expect(pill).toContainText('1 blocking');
  await pill.click();
  await expect(cards).toHaveCount(1);
  await expect(cards.first()).toContainText("isn't published yet");
  await pill.click();
  await expect(cards).toHaveCount(2);

  await page.locator('input.pr-search').fill('status:blocking');
  await expect(cards).toHaveCount(1);

  // The row preview lists the blocker first even though the server appended it
  // after the cautions, so a truncated preview never cuts off the blocker.
  const row = page.locator('.card-li:has(.card-shell[data-pr="7"])');
  await row.locator('.card-expand').click();
  const previewFlags = row.locator('.card-preview .pv-caution');
  await expect(previewFlags).toHaveCount(4);
  await expect(previewFlags.first()).toHaveClass(/blocking/);
  await expect(previewFlags.first()).toContainText('rook-ceph-operator');
});

test('the review shows the blocker first, deep-linked to the workload, and a verdict per image', async ({ page }) => {
  await stubApi(page);
  await page.goto('/#/pr/7');

  // The findings column turns red and leads with the blocker (the server listed
  // it last), which carries an inline "blocking" badge and names the Deployment.
  const column = page.locator('.ov-section', { has: page.locator('h3', { hasText: 'Cautions' }) });
  await expect(column).toHaveClass(/fail-section/);
  const flags = column.locator('.flag');
  await expect(flags).toHaveCount(4);
  await expect(flags.first()).toHaveClass(/blocking/);
  await expect(flags.first().locator('.badge.blocking')).toContainText('blocking');
  await expect(flags.first()).toContainText('Deployment rook-ceph/rook-ceph-operator');

  // Every image with a new reference says how the registry answered.
  const img = (name: string) => page.locator('.img-change', { hasText: name });
  await expect(img('ghcr.io/rook/ceph').locator('.img-upstream.missing')).toContainText('not found');
  await expect(img('thelounge').locator('.img-upstream.found')).toContainText('found');
  await expect(img('private/app').locator('.img-upstream.unverified')).toContainText('unverified');

  // The tree marks the Summary and the affected leaf in red, not amber.
  await expect(page.locator('.tree-summary .summary-caution.blocking')).toBeVisible();
  await expect(page.locator('.tree-item', { hasText: 'rook-ceph-operator' }).locator('.leaf-blocking')).toBeVisible();

  // The blocker is a button: clicking it opens the Deployment's diff. Its sticky
  // header counts each tier on its own badge — one blocker and one caution here
  // read "blocking" + "caution", never "blocking 2".
  await flags.first().click();
  await expect(page).toHaveURL(/#\/pr\/7\/r0$/);
  const header = page.locator('.res-header', { hasText: 'rook-ceph-operator' });
  await expect(header.locator('.badge.blocking')).toHaveText(/^\s*blocking\s*$/);
  await expect(header.locator('.badge.caution')).toHaveText(/^\s*caution\s*$/);
});
