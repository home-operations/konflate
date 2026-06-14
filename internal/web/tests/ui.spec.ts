import { test, expect, type Page } from '@playwright/test';
import { samplePRs, diffEnvelope } from './fixtures';
import type { Meta, PRStatus } from '../src/lib/types';

const defaultMeta: Meta = {
  forge: 'github',
  repo: 'acme/home-ops',
  repoUrl: 'https://github.com/acme/home-ops',
  version: '1.2.3',
  refreshIntervalSeconds: 1800,
  features: { checks: true }, // authenticated instance: CI checks shown
};

// Stub the konflate API (and silence the websocket) so the UI renders
// deterministically with no backend.
async function stubApi(page: Page, meta: Meta = defaultMeta, prs: PRStatus[] = samplePRs) {
  await page.route('**/api/meta', (route) => route.fulfill({ json: meta }));
  await page.route('**/api/prs', (route) => route.fulfill({ json: prs }));
  await page.route('**/api/prs/142/diff', (route) => route.fulfill({ json: diffEnvelope }));
  // The row expander hits the lean summary endpoint; the fixture envelope serves
  // both (the UI reads the same headline fields).
  await page.route('**/api/prs/142/summary', (route) => route.fulfill({ json: diffEnvelope }));
  await page.routeWebSocket('**/ws', () => {
    /* accept; no live events needed */
  });
}

test('PR list shows the forge CI check status per PR (green / amber / red)', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');
  const card = (n: number) => page.locator(`.card-shell[data-pr="${n}"]`);
  await card(142).waitFor();
  await expect(card(142).locator('.check-success')).toBeVisible(); // 4/4 passed
  await expect(card(138).locator('.check-pending')).toBeVisible(); // still running
  await expect(card(131).locator('.check-failure')).toBeVisible(); // one failed
  // The check sits in the title row, right next to the title — not down in the
  // meta row among the signal badges.
  await expect(card(142).locator('.card-top .check-success')).toBeVisible();
  await expect(card(142).locator('.card-meta .check')).toHaveCount(0);
});

test('anonymous instance hides forge CI checks (features.checks=false)', async ({ page }) => {
  // No forge auth server-side ⇒ meta.features.checks is false ⇒ konflate doesn't
  // poll CI status, and the UI hides the check pill it would otherwise show even
  // if a rollup were present (see api.Features).
  await stubApi(page, { ...defaultMeta, features: { checks: false } });
  await page.goto('/');
  const card = (n: number) => page.locator(`.card-shell[data-pr="${n}"]`);
  await card(142).waitFor();
  // The fixtures carry check rollups on these PRs, but with the feature off none
  // of the pills render anywhere.
  await expect(page.locator('.check')).toHaveCount(0);
});

test('list → review → single-page flow', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');

  // Topbar shows the forge logo + repo (linked to its forge page) and the
  // auto-update indicator — there is no manual refresh button.
  await expect(page.locator('.repo')).toContainText('acme/home-ops');
  await expect(page).toHaveTitle('Konflate - acme/home-ops'); // repo rides in the browser tab
  await expect(page.locator('.repo svg[role="img"]')).toBeVisible();
  await expect(page.locator('a.repo')).toHaveAttribute('href', 'https://github.com/acme/home-ops');
  await expect(page.locator('a.repo')).toHaveAttribute('target', '_blank');
  await expect(page.locator('.actions .auto')).toContainText('30m');
  await expect(page.locator('.actions .btn', { hasText: 'Refresh' })).toHaveCount(0);

  // Landing list: cards with per-PR signal badges.
  await expect(page.locator('.card')).toHaveCount(3);
  const card142 = page.locator('.card-shell[data-pr="142"]');
  // The forge link in a list row is icon-only — the "#<n>" text is dropped to
  // keep the row uncluttered; the number lives in its tooltip (asserted below).
  await expect(card142.locator('.forge-link')).toBeVisible();
  await expect(card142.locator('.forge-link')).not.toContainText('#142');
  await expect(card142.locator('.card-title')).toHaveText(
    'feat(rook-ceph): bump the rook-ceph operator and cluster chart to v1.15.0',
  );
  await expect(card142.locator('.badge.danger').first()).toBeVisible();
  await expect(card142.locator('.ago').first()).toHaveText(/ago|just now/); // humanized timestamps
  // Author avatar renders when present; a PR without one falls back to the icon.
  await expect(card142.locator('img.avatar')).toBeVisible();
  await expect(page.locator('.card-shell[data-pr="138"]').locator('img.avatar')).toHaveCount(0);
  // PR age: a clock icon + relative time (the full "Opened …" date is in the
  // title, so the word itself is dropped), plus a colored label dot.
  await expect(card142.locator('.ago[title^="Opened"]')).toBeVisible();
  await expect(card142.locator('.label-dot')).toBeVisible();
  // Every pill on the meta row — signal badges and labels — shares one height;
  // left to line-height/font metrics they drift apart across browsers.
  const pillHeights = await card142
    .locator('.badge, .label')
    .evaluateAll((els) => els.map((el) => el.getBoundingClientRect().height));
  expect(pillHeights.length).toBeGreaterThanOrEqual(2);
  for (const h of pillHeights) expect(Math.abs(h - pillHeights[0])).toBeLessThanOrEqual(0.5);
  // Signal-icon tooltips spell out the count (images is singular at 1).
  await expect(card142.locator('.badge.muted')).toHaveAttribute('title', '3 resource changes');
  await expect(
    card142.locator('.badge:not(.caution):not(.danger):not(.muted):not(.warn)'),
  ).toHaveAttribute('title', '1 image change');
  // A PR-icon link, right of the badges, opens the PR on its forge (named in the
  // tooltip). It's a sibling of the card button, so scope to the shell.
  const forge142 = page.locator('.card-shell[data-pr="142"]').locator('.forge-link');
  await expect(forge142).toHaveAttribute('href', 'https://github.com/acme/home-ops/pull/142');
  await expect(forge142).toHaveAttribute('target', '_blank');
  await expect(forge142).toHaveAttribute('title', 'Open PR #142 on GitHub');
  // Hovering the link recolors the glyph only — no background box (it read as
  // a floating button beside the inert signal badges).
  await forge142.hover();
  const forgeHoverBg = await forge142.evaluate((el) => getComputedStyle(el).backgroundColor);
  expect(['rgba(0, 0, 0, 0)', 'transparent']).toContain(forgeHoverBg);

  // Open a PR → the single-page review lands on the Summary (impact, warnings,
  // image changes, render failures), with the tree rail alongside it.
  await card142.click();
  await expect(page).toHaveURL(/#\/pr\/142$/);
  await expect(page.locator('.impact')).toContainText('resources');
  await expect(page.locator('.flag.caution', { hasText: 'StatefulSet' })).toBeVisible();
  await expect(page.locator('.img-list')).toContainText('ghcr.io/rook/ceph');
  await expect(page.locator('.flag', { hasText: 'plex' })).toBeVisible();
  // The forge CI check rides next to the PR title on the review header too
  // (#142 is 4/4 green).
  await expect(page.locator('.review-title .check-success')).toBeVisible();
  // The PR link lives in the meta trailer (beside the last-rendered time), as an
  // icon — no "#142" text, and no longer a direct child next to the title.
  const headerLink = page.locator('.rt-meta .forge-link');
  await expect(headerLink).toHaveAttribute('href', 'https://github.com/acme/home-ops/pull/142');
  await expect(headerLink).toHaveAttribute('title', 'Open PR #142 on GitHub');
  await expect(headerLink).not.toContainText('#142');
  await expect(page.locator('.review-title > .forge-link')).toHaveCount(0);
  // The tree: a Summary node (selected by default) + one leaf per changed
  // resource. A caution surfaces a marker on the Summary node.
  await expect(page.locator('.tree .tree-summary')).toHaveClass(/selected/);
  await expect(page.locator('.tree-summary .summary-caution')).toBeVisible();
  await expect(page.locator('.tree .tree-item')).toHaveCount(3);

  // Click a resource → its diff renders in the same view (no tab switch) and the
  // Summary node deselects.
  await page.locator('.tree .tree-item').first().click();
  await expect(page).toHaveURL(/#\/pr\/142\/r0$/);
  await expect(page.locator('.tree .tree-summary')).not.toHaveClass(/selected/);
  await expect(page.locator('table.diff tr.row-add').first()).toBeVisible();
  await expect(page.locator('table.diff tr.row-del').first()).toBeVisible();

  // Click the Summary node → back to the overview panel.
  await page.locator('.tree .tree-summary').click();
  await expect(page).toHaveURL(/#\/pr\/142\/summary$/);
  await expect(page.locator('.impact')).toBeVisible();
});

test('landing health summary + non-default base branch tag', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');

  // One-line health summary over the open set: 3 open, #142 carrying cautions,
  // plus the recently-merged shelf count.
  const summary = page.locator('.list-summary');
  await expect(summary).toContainText('3 open');
  await expect(summary.locator('.sum-pill.caution')).toContainText('1 caution');
  await expect(summary.locator('.sum-pill.merged')).toContainText('1 merged');

  // Most PRs target main, so only #138 (→ staging) is flagged with a base tag.
  await expect(page.locator('.card-shell[data-pr="138"]').locator('.base-tag')).toContainText('staging');
  await expect(page.locator('.card-shell[data-pr="142"]').locator('.base-tag')).toHaveCount(0);
});

test('a failed refresh keeps the diff and flags it (list badge + review strip)', async ({ page }) => {
  await page.route('**/api/meta', (r) => r.fulfill({ json: defaultMeta }));
  await page.route('**/api/prs', (r) =>
    r.fulfill({ json: [{ ...samplePRs[0], refreshError: 'forge unreachable' }] }),
  );
  await page.route('**/api/prs/142/diff', (r) =>
    r.fulfill({ json: { ...diffEnvelope, refreshError: 'forge unreachable' } }),
  );
  await page.routeWebSocket('**/ws', () => {});
  await page.goto('/');

  // The card flags the failed refresh but still shows the last-good signal badges.
  const card = page.locator('.card-shell[data-pr="142"]');
  await expect(card.locator('.badge[title*="refresh"]')).toBeVisible();
  await expect(card.locator('.badge.danger').first()).toBeVisible();

  // The review shows a banner and still renders the kept diff (tree intact).
  await card.click();
  await expect(page.locator('.refresh-strip')).toContainText("Couldn't refresh");
  await expect(page.locator('.tree .tree-item')).toHaveCount(3);
});

test('merged PRs are reached via the merged pill, not shown by default', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');

  // Landing shows only the 3 open PRs; the merged one isn't in the default view.
  await expect(page.locator('.card')).toHaveCount(3);
  await expect(page.locator('.card-shell[data-pr="128"]')).toHaveCount(0);

  // The merged pill carries the count; clicking it switches to the merged view.
  const mergedPill = page.locator('.sum-pill.merged');
  await expect(mergedPill).toContainText('1');
  await mergedPill.click();
  const mergedCard = page.locator('.card-shell.merged[data-pr="128"]');
  await expect(mergedCard).toBeVisible();
  // No redundant "merged" tag — the dimmed card, purple dot, and "merged …"
  // timestamp already convey it.
  await expect(mergedCard.locator('.ago')).toContainText('merged');

  // Opening a merged PR shows the frozen-diff banner.
  await page.route('**/api/prs/128/diff', (route) =>
    route.fulfill({ json: { status: 'ready', pr: samplePRs[3], diff: { ...diffEnvelope.diff, prNumber: 128 } } }),
  );
  await mergedCard.locator('.card').click();
  await expect(page).toHaveURL(/#\/pr\/128$/);
  await expect(page.locator('.merged-strip')).toContainText('frozen');
});

test('filter-excluded PRs are hidden: a pill, a grey dot, out of the default view, not rendered', async ({ page }) => {
  const tracked = {
    number: 1, title: 'tracked', author: 'x', state: 'open', open: true, draft: false,
    headRef: 'h', headSha: 'h1', baseRef: 'main', labels: [], url: 'https://github.com/acme/home-ops/pull/1',
    status: 'ready', updatedAt: '2026-06-04T12:00:00Z', signals: { resources: 1, caution: 0, images: 0, failures: 0 },
  };
  const hidden = {
    number: 9, title: 'external fork', author: 'outsider', state: 'open', open: true, draft: false,
    headRef: 'patch', headSha: 'f9', baseRef: 'main', labels: [], url: 'https://github.com/outsider/home-ops/pull/9',
    status: 'pending', updatedAt: '2026-06-04T12:00:00Z', hidden: true,
  };
  await page.route('**/api/meta', (r) => r.fulfill({ json: defaultMeta }));
  await page.route('**/api/prs', (r) => r.fulfill({ json: [tracked, hidden] }));
  await page.route('**/api/prs/9/diff', (r) => r.fulfill({ json: { status: 'pending', pr: hidden, hidden: true } }));
  await page.routeWebSocket('**/ws', () => {});
  await page.goto('/');

  // Default view shows only the tracked PR; the hidden one is excluded.
  await expect(page.locator('.card')).toHaveCount(1);
  await expect(page.locator('.card-shell[data-pr="9"]')).toHaveCount(0);

  // The hidden pill carries the count; clicking it reveals the hidden PR, greyed.
  const hiddenPill = page.locator('.sum-pill.hidden');
  await expect(hiddenPill).toContainText('1');
  await hiddenPill.click();
  const hiddenCard = page.locator('.card-shell[data-pr="9"]');
  await expect(hiddenCard).toBeVisible();
  await expect(hiddenCard.locator('.dot-hidden')).toBeVisible();
  await expect(page.locator('.card')).toHaveCount(1); // only the hidden PR now

  // Opening it explains it isn't rendered, rather than spinning forever.
  await hiddenCard.locator('.card').click();
  await expect(page).toHaveURL(/#\/pr\/9$/);
  await expect(page.locator('.review-body')).toContainText('Excluded by the PR filter');
});

// bulkPRs builds `open` open PRs (highest numbers, newest) plus `merged` merged
// ones below them — the real fixtures only carry three, under the smallest page
// size. createdAt increases with the number so created-desc sorts highest first,
// making the page contents deterministic.
function bulkPRs({ open, merged = 0 }: { open: number; merged?: number }): PRStatus[] {
  const total = open + merged;
  return Array.from({ length: total }, (_, i) => {
    const number = total - i; // newest (highest) first
    const isOpen = i < open;
    return {
      number,
      title: `chore(deps): bump dependency ${number}`,
      author: 'renovate[bot]',
      state: isOpen ? 'open' : 'closed',
      open: isOpen,
      draft: false,
      headRef: `renovate/dep-${number}`,
      headSha: `sha${number}`,
      baseRef: 'main',
      createdAt: new Date(Date.UTC(2026, 5, 1, 0, number)).toISOString(),
      closedAt: isOpen ? undefined : new Date(Date.UTC(2026, 5, 2, 0, number)).toISOString(),
      labels: [],
      url: `https://github.com/acme/home-ops/pull/${number}`,
      status: 'ready',
      updatedAt: '2026-06-04T12:00:00Z',
    } satisfies PRStatus;
  });
}

test('list pagination: default 10 per page, prev/next, and a size picker', async ({ page }) => {
  await stubApi(page, defaultMeta, bulkPRs({ open: 25 }));
  await page.goto('/');

  // Default page size is 10 (the lowest), newest first.
  await expect(page.locator('.card')).toHaveCount(10);
  await expect(page.locator('.pager-count')).toHaveText('1–10 of 25');
  await expect(page.locator('.pager .pager-page')).toHaveText('Page 1 of 3');
  await expect(page.locator('.card-shell[data-pr="25"]')).toBeVisible();
  await expect(page.locator('.card-shell[data-pr="15"]')).toHaveCount(0); // page 2's top

  // Next → page 2: the URL carries the page, and the window slides.
  await page.locator('.pager .pager-btn[aria-label="Next page"]').click();
  await expect(page).toHaveURL(/#\/page\/2$/);
  await expect(page.locator('.pager .pager-page')).toHaveText('Page 2 of 3');
  await expect(page.locator('.pager-count')).toHaveText('11–20 of 25');
  await expect(page.locator('.card-shell[data-pr="15"]')).toBeVisible();
  await expect(page.locator('.card-shell[data-pr="25"]')).toHaveCount(0);

  // Bumping the size to 50 shows all 25 on one page, drops the prev/next nav, and
  // resets the URL to the canonical page 1.
  await page.locator('.page-size select').selectOption('50');
  await expect(page).toHaveURL(/#\/$/);
  await expect(page.locator('.card')).toHaveCount(25);
  await expect(page.locator('.pager-nav')).toHaveCount(0);
  await expect(page.locator('.pager-count')).toHaveText('1–25 of 25');
});

test('list pagination: a compact pager beside expand-all pages from the top', async ({ page }) => {
  await stubApi(page, defaultMeta, bulkPRs({ open: 25 }));
  await page.goto('/');

  // The top pager (in the summary row's right cluster) mirrors the bottom one and
  // pages the list without scrolling to its end.
  const topNav = page.locator('.list-summary-end .pager-nav');
  await expect(topNav.locator('.pager-page')).toHaveText('Page 1 of 3');
  await topNav.locator('.pager-btn[aria-label="Next page"]').click();
  await expect(page).toHaveURL(/#\/page\/2$/);
  await expect(topNav.locator('.pager-page')).toHaveText('Page 2 of 3');
  await expect(page.locator('.pager .pager-page')).toHaveText('Page 2 of 3'); // bottom stays in sync
});

test('render-failure signals: a "failure" pill, a red card edge, and the badge left of images', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');

  // A red pill beside "open" counts PRs that failed to render and filters to them.
  const failurePill = page.locator('.sum-pill.failure');
  await expect(failurePill).toContainText('1 failure');
  await failurePill.click();
  await expect(page.locator('.card')).toHaveCount(1);
  const card = page.locator('.card-shell[data-pr="142"]');
  await expect(card).toBeVisible();

  // The failing card carries a red edge (its own class), like a caution card's amber one.
  await expect(card).toHaveClass(/failure/);

  // The render-failure badge (danger) and the caution badge both sit LEFT of the
  // image-changes badge (the bare `.badge`) — failures was previously to its right.
  const classes = await card.locator('.badges .badge').evaluateAll((els) => els.map((el) => el.className.trim()));
  const dangerIdx = classes.findIndex((c) => c.includes('danger'));
  const cautionIdx = classes.findIndex((c) => c.includes('caution'));
  const imagesIdx = classes.indexOf('badge');
  expect(dangerIdx).toBeGreaterThanOrEqual(0);
  expect(imagesIdx).toBeGreaterThan(dangerIdx);
  expect(imagesIdx).toBeGreaterThan(cautionIdx);
});

test('list pagination: deep-links a page and clamps an out-of-range page', async ({ page }) => {
  await stubApi(page, defaultMeta, bulkPRs({ open: 25 }));

  await page.goto('/#/page/2');
  await expect(page.locator('.pager .pager-page')).toHaveText('Page 2 of 3');
  await expect(page.locator('.card-shell[data-pr="15"]')).toBeVisible();

  // A page past the end clamps to the last page (and rewrites the URL to match).
  await page.goto('/#/page/99');
  await expect(page.locator('.pager .pager-page')).toHaveText('Page 3 of 3');
  await expect(page).toHaveURL(/#\/page\/3$/);
  await expect(page.locator('.card')).toHaveCount(5); // 25 → pages of 10/10/5
});

test('list pagination stays coherent with the filter pills', async ({ page }) => {
  await stubApi(page, defaultMeta, bulkPRs({ open: 22, merged: 13 }));
  await page.goto('/');

  // Default (open) view: the pager counts the open set — the same total the open
  // pill shows — not every tracked PR.
  await expect(page.locator('.sum-pill.merged')).toContainText('13 merged');
  await expect(page.locator('.pager-count')).toHaveText('1–10 of 22');

  // Page in, then switch to the merged pill: the page resets to 1 and the count
  // tracks the merged set, so "of N" matches the merged pill's count (cohesion).
  await page.locator('.pager .pager-btn[aria-label="Next page"]').click();
  await expect(page.locator('.pager .pager-page')).toHaveText('Page 2 of 3');
  await page.locator('.sum-pill.merged').click();
  await expect(page).toHaveURL(/#\/$/); // back to page 1
  await expect(page.locator('.pager-count')).toHaveText('1–10 of 13');
  await expect(page.locator('.pager .pager-page')).toHaveText('Page 1 of 2');
});

test('split diff renders two balanced columns (regression)', async ({ page }) => {
  await stubApi(page);
  await page.goto('/#/pr/142/r0');
  await page.getByRole('button', { name: 'Split' }).first().click();
  await expect(page.locator('table.diff.split').first()).toBeVisible();
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
  await page.goto('/#/pr/142/r0');
  await page.getByRole('button', { name: 'Unified' }).first().click();

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

  // Rendering: a spinner fills the right (disclosure) column — no "rendering" text.
  const running = page.locator('.card-shell', { hasText: 'busy pr' });
  await expect(running.locator('.card-state .kspin')).toBeVisible(); // thematic wheel spinner
  await expect(running.locator('.card-state')).toHaveAttribute('title', 'Rendering…');
  await expect(running).not.toContainText('rendering');

  // Pending: a queued icon, again with no text — the tooltip carries it.
  const waiting = page.locator('.card-shell', { hasText: 'waiting pr' });
  await expect(waiting.locator('.card-state')).toHaveAttribute('title', 'Queued to render');
  await expect(waiting).not.toContainText('queued');
});

test('the PR list loads without a loading mascot, then the list arrives', async ({ page }) => {
  await page.route('**/api/meta', (r) => r.fulfill({ json: defaultMeta }));
  await page.route('**/api/prs', async (r) => {
    await new Promise((res) => setTimeout(res, 400)); // hold the list briefly
    await r.fulfill({ json: samplePRs });
  });
  await page.routeWebSocket('**/ws', () => {});
  await page.goto('/');

  // No loading mascot flashes during the (held) load — the pane stays quiet…
  await expect(page.locator('.smasher')).toHaveCount(0);
  // …and the data still lands.
  await expect(page.locator('.card')).toHaveCount(3);
});

test('review body shows a status message (not a spinner) while a diff renders', async ({ page }) => {
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
  // A plain text status for a genuine server-side render — no animated mascot.
  await expect(page.locator('.loading-center')).toContainText('Rendering');
  await expect(page.locator('.smasher')).toHaveCount(0);
});

test('deep link opens a specific resource diff', async ({ page }) => {
  await stubApi(page);
  await page.goto('/#/pr/142/r0');
  await expect(page.locator('[data-sel="r0"] .res-title')).toContainText('rook-ceph-operator');
  await expect(page.locator('[data-sel="r0"] table.diff tr.row-add')).toBeVisible();
});

test('keyboard: j steps from Summary into resources, o returns to Summary', async ({ page }) => {
  await stubApi(page);
  await page.goto('/#/pr/142');
  // Navigation needs the diff loaded; the default landing is the Summary.
  await page.locator('.impact').waitFor();
  await page.locator('body').press('j');
  await expect(page).toHaveURL(/#\/pr\/142\/r0$/);
  await page.locator('body').press('j');
  await expect(page).toHaveURL(/#\/pr\/142\/r1$/);
  // o jumps straight back to the Summary panel.
  await page.locator('body').press('o');
  await expect(page).toHaveURL(/#\/pr\/142\/summary$/);
  await expect(page.locator('.impact')).toBeVisible();
});

test('auto-update indicator reflects the configured interval, no refresh button', async ({ page }) => {
  await stubApi(page, { forge: 'forgejo', repo: 'me/home-ops', refreshIntervalSeconds: 600, features: { checks: true } });
  await page.goto('/');
  await expect(page.locator('.actions .auto')).toContainText('10m');
  await expect(page.locator('.actions .btn', { hasText: 'Refresh' })).toHaveCount(0);
  // No repoUrl in this meta → the repo renders as plain text, not a link.
  await expect(page.locator('div.repo')).toContainText('me/home-ops');
  await expect(page.locator('a.repo')).toHaveCount(0);
  await page.screenshot({ path: 'screenshots/konflate-auto.png' });
});

test('filter narrows the PR list', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');
  await page.getByPlaceholder('Filter pull requests…').fill('plex');
  await expect(page.locator('.card')).toHaveCount(1);
  await expect(page.locator('.card-shell')).toHaveAttribute('data-pr', '131');
});

test('the filter understands facet tokens (status:/author:/base:/label:)', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');
  const input = page.locator('.pr-search');

  await input.fill('status:caution');
  await expect(page.locator('.card')).toHaveCount(1);
  await expect(page.locator('.card-shell')).toHaveAttribute('data-pr', '142');

  // Tokens AND together, and with free text.
  await input.fill('author:octocat base:staging');
  await expect(page.locator('.card')).toHaveCount(1);
  await expect(page.locator('.card-shell')).toHaveAttribute('data-pr', '138');

  await input.fill('label:media plex');
  await expect(page.locator('.card')).toHaveCount(1);
  await expect(page.locator('.card-shell')).toHaveAttribute('data-pr', '131');

  // The clear button empties the query and restores the list.
  await page.locator('.search-box .clear-btn').click();
  await expect(page.locator('.card')).toHaveCount(3);
  await expect(input).toHaveValue('');
});

test('Ctrl+K palette: search, keyboard nav, open, recents', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');

  // Ctrl+K opens it from anywhere; the input is focused.
  await page.keyboard.press('Control+k');
  const dialog = page.getByRole('dialog', { name: 'Search pull requests' });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('textbox')).toBeFocused();

  // Risk floats first with no query: the riskiest PR (failures + cautions) leads.
  await expect(dialog.locator('.palette-row .row-title').first()).toContainText('rook-ceph');
  // Signal preview rides on the row.
  await expect(dialog.locator('.palette-row').first().locator('.badge.caution').first()).toBeVisible();

  // Typing narrows (and highlights the match); Enter opens the active row.
  await dialog.getByRole('textbox').fill('plex');
  await expect(dialog.locator('.palette-row')).toHaveCount(1);
  await expect(dialog.locator('.palette-row mark')).toHaveText('plex');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#\/pr\/131$/);
  await expect(dialog).toHaveCount(0);

  // Reopening (from the review screen) shows the committed query under Recent.
  await page.keyboard.press('Control+k');
  await expect(dialog.locator('.group-label').first()).toHaveText('Recent');
  await expect(dialog.locator('.palette-row .row-title').first()).toHaveText('plex');

  // Escape closes without navigating.
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  await expect(page).toHaveURL(/#\/pr\/131$/);
});

test('Ctrl+K palette: facet tokens filter; arrows move the cursor', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');
  await page.keyboard.press('Control+k');
  const dialog = page.getByRole('dialog', { name: 'Search pull requests' });

  await dialog.getByRole('textbox').fill('author:octocat');
  await expect(dialog.locator('.palette-row')).toHaveCount(3);
  // Risk-first ordering: #131 (render failure) leads octocat's PRs.
  await expect(dialog.locator('.palette-row').nth(0)).toContainText('#131');
  await expect(dialog.locator('.palette-row.active')).toContainText('#131');

  // ArrowDown moves the selection; Enter opens that PR.
  await page.keyboard.press('ArrowDown');
  await expect(dialog.locator('.palette-row.active')).toContainText('#138');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#\/pr\/138$/);
});

test('summary pills filter by status; the sort selector reorders', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');

  // Default sort: created desc → #138 (06-03) > #142 (06-01) > #131 (05-30).
  const ids = page.locator('.cards .card-shell');
  await expect(ids.nth(0)).toHaveAttribute('data-pr', '138');
  await expect(ids.nth(1)).toHaveAttribute('data-pr', '142');
  await expect(ids.nth(2)).toHaveAttribute('data-pr', '131');

  // Refreshed sort: last render desc → #142 (12:00) > #138 (11:30) > #131 (10:00).
  await page.locator('.sort select').selectOption('refreshed');
  await expect(ids.nth(0)).toHaveAttribute('data-pr', '142');
  await expect(ids.nth(1)).toHaveAttribute('data-pr', '138');

  // The caution pill narrows to the one PR carrying cautions; clicking again
  // clears it.
  const caution = page.locator('.sum-pill', { hasText: 'caution' });
  await caution.click();
  await expect(caution).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('.card')).toHaveCount(1);
  await expect(page.locator('.card-shell')).toHaveAttribute('data-pr', '142');
  await caution.click();
  await expect(page.locator('.card')).toHaveCount(3);

  // merged → only merged PRs show (the open list is replaced); counts stay visible.
  await page.locator('.sum-pill', { hasText: 'merged' }).click();
  await expect(page.locator('.card')).toHaveCount(1);
  await expect(page.locator('.card-shell')).toHaveAttribute('data-pr', '128');
  await expect(page.locator('.list-summary')).toContainText('3 open');

  // Status filter + a text query that excludes everything → the no-match state.
  await page.getByPlaceholder('Filter pull requests…').fill('plex');
  await expect(page.locator('.card')).toHaveCount(0);
  await expect(page.locator('.empty')).toContainText('match your filter');
});

test('the open pill filters back to all open and hides the merged shelf', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');

  // From a caution-filtered view, the open pill returns the full open set.
  await page.locator('.sum-pill', { hasText: 'caution' }).click();
  await expect(page.locator('.card')).toHaveCount(1);

  const open = page.locator('.sum-pill', { hasText: 'open' });
  await open.click();
  await expect(open).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('.card')).toHaveCount(3); // all three open PRs
  await expect(page.locator('.merged-cards')).toHaveCount(0); // merged shelf hidden

  // Toggling it off returns to the unfiltered view.
  await open.click();
  await expect(open).toHaveAttribute('aria-pressed', 'false');
  await expect(page.locator('.card')).toHaveCount(3);
});

test('list sort: direction toggle, and changing field resets to its natural order', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');
  const ids = page.locator('.cards .card-shell');
  const dir = page.locator('.sort-dir');

  // created desc (default) → newest first.
  await expect(ids.nth(0)).toHaveAttribute('data-pr', '138');
  await expect(dir).toHaveAttribute('aria-label', 'Sort: newest first');

  // Toggle → ascending (oldest first) → the order reverses.
  await dir.click();
  await expect(dir).toHaveAttribute('aria-label', 'Sort: oldest first');
  await expect(ids.nth(0)).toHaveAttribute('data-pr', '131');
  await expect(ids.nth(2)).toHaveAttribute('data-pr', '138');

  // Switching field resets to that field's natural direction: name → A→Z.
  await page.locator('.sort select').selectOption('name');
  await expect(dir).toHaveAttribute('aria-label', 'Sort: A → Z');
  // A→Z by title: "chore…"(#138) < "feat…"(#142) < "fix…"(#131).
  await expect(ids.nth(0)).toHaveAttribute('data-pr', '138');
  await expect(ids.nth(2)).toHaveAttribute('data-pr', '131');
});

test('opening an already-rendered PR does not flash the loading spinner', async ({ page }) => {
  await stubApi(page);
  // The stubbed (instant) ready diff resolves before the spinner delay, so the
  // Summary renders and the loading mascot never appears.
  await page.goto('/#/pr/142');
  await expect(page.locator('[data-sel="summary"] .impact')).toBeVisible();
  await expect(page.locator('.loading-center')).toHaveCount(0);
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
  await page.goto('/#/pr/142/r0');
  const title = page.locator('[data-sel="r0"] .res-title');
  await title.waitFor();
  const box = await title.boundingBox();
  // The bug squeezed the title into a ~0-width flex column, so break-all stacked
  // it one character per line (~28 lines tall). Fixed, it's a readable 1–2 rows.
  expect(box?.height ?? 999).toBeLessThan(120);
  expect(box?.width ?? 0).toBeGreaterThan(150);
});

test('mobile: a wide diff line keeps the diff header within the viewport (regression)', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await stubApi(page);
  // A long, unbreakable line: diff cells wrap (white-space: pre-wrap), but
  // word-break: break-word doesn't shrink their *min-content*, so without
  // min-width: 0 up the flex/grid chain this floored the whole column at the
  // line's full width — pushing the diff header (filename + caution badge) and
  // the mobile switcher off the right edge.
  const wide = structuredClone(diffEnvelope);
  wide.diff.resources[0].unified.push({
    kind: 'add',
    newNo: 99,
    html: 'image: ghcr.io/org/some/really/long/registry/path/that/keeps/going:v1.2.3-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  });
  await page.route('**/api/prs/142/diff', (route) => route.fulfill({ json: wide }));
  // r2 = StatefulSet default/postgres — the removed resource whose caution badge
  // sat on the header's right edge (the part that got clipped).
  await page.goto('/#/pr/142/r2');
  const header = page.locator('[data-sel="r2"] .res-header');
  await header.waitFor();

  // The header fills the viewport but never exceeds it (was ~448 on a 390 screen).
  const box = await header.boundingBox();
  expect(box?.width ?? 999).toBeLessThanOrEqual(391);
  // The caution badge stays fully inside the viewport — its right edge no longer
  // pushed off-screen.
  const badge = await page.locator('[data-sel="r2"] .res-header .badge.caution').boundingBox();
  expect((badge?.x ?? 0) + (badge?.width ?? 0)).toBeLessThanOrEqual(390);
  // And the page never scrolls sideways.
  const review = await page.evaluate(() => {
    const el = document.querySelector('.review') as HTMLElement;
    return { client: el.clientWidth, scroll: el.scrollWidth };
  });
  expect(review.scroll).toBeLessThanOrEqual(review.client + 1);
});

test('mobile: a long PR title wraps to two lines instead of truncating', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await stubApi(page);
  await page.goto('/');
  const title = page.locator('.card-shell[data-pr="142"]').locator('.card-title');
  await title.waitFor();
  // Two-line clamp is engaged on mobile (single-line truncation uses nowrap).
  await expect(title).toHaveCSS('white-space', 'normal');
  const box = await title.boundingBox();
  expect(box?.height ?? 0).toBeGreaterThan(20); // taller than one line ⇒ it wrapped
  expect(box?.height ?? 999).toBeLessThan(60); // but clamped — never a tall stack
});

test('mobile: a switcher cycles Summary + resources (the tree rail is hidden)', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await stubApi(page);
  await page.goto('/#/pr/142');
  const sw = page.locator('.diff-switcher');
  await expect(sw).toBeVisible();
  // Lands on the Summary: item 1 of 4 (Summary + 3 resources); Previous disabled.
  await expect(sw.locator('.switcher-pos')).toHaveText('1/4');
  await expect(sw.locator('.switcher-name')).toHaveText('Summary');
  await expect(sw.getByRole('button', { name: 'Previous' })).toBeDisabled();

  // Next steps into the first resource's diff.
  await sw.getByRole('button', { name: 'Next' }).click();
  await expect(page).toHaveURL(/#\/pr\/142\/r0$/);
  await expect(sw.locator('.switcher-pos')).toHaveText('2/4');
  await expect(sw.locator('.switcher-name')).toContainText('rook-ceph-operator');

  // …through to the last resource, where Next disables.
  await sw.getByRole('button', { name: 'Next' }).click();
  await expect(page).toHaveURL(/#\/pr\/142\/r1$/);
  await sw.getByRole('button', { name: 'Next' }).click();
  await expect(page).toHaveURL(/#\/pr\/142\/r2$/);
  await expect(sw.locator('.switcher-pos')).toHaveText('4/4');
  await expect(sw.getByRole('button', { name: 'Next' })).toBeDisabled();

  // Previous walks back toward the Summary.
  await sw.getByRole('button', { name: 'Previous' }).click();
  await expect(page).toHaveURL(/#\/pr\/142\/r1$/);
});

test('mobile: split view is unavailable; diffs render unified', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await stubApi(page);
  await page.goto('/#/pr/142/r0');
  await expect(page.locator('table.diff.unified').first()).toBeVisible();
  await expect(page.locator('table.diff.split')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Split' })).toHaveCount(0);
});

test('mobile: topbar keeps the repo name and icon buttons are tappable', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await stubApi(page);
  await page.goto('/');
  // The wordmark + auto-label collapse so the repo name isn't crowded out.
  await expect(page.locator('.repo')).toContainText('acme/home-ops');
  await expect(page.locator('.brand .wordmark')).toBeHidden();
  await expect(page.locator('.auto .auto-text')).toBeHidden();
  await expect(page.locator('.auto svg')).toBeVisible(); // the clock icon stays
  // Icon buttons are ≥40px tall for thumbs, and the auto-refresh pill matches
  // them (same box size) once it collapses to just the clock icon.
  const btn = await page.locator('.actions .btn-icon').last().boundingBox();
  const auto = await page.locator('.actions .auto').boundingBox();
  expect(btn?.height ?? 0).toBeGreaterThanOrEqual(40);
  expect(Math.abs((auto?.height ?? 0) - (btn?.height ?? 0))).toBeLessThanOrEqual(1);
  expect(Math.abs((auto?.width ?? 0) - (btn?.width ?? 0))).toBeLessThanOrEqual(1);
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
  await page.goto('/#/pr/142/r0');
  await page.locator('[data-sel="r0"] .res-header .copy-btn').click();
  const copied2 = await page.evaluate(() => (window as unknown as { __copied: string[] }).__copied);
  expect(copied2).toContain('Deployment rook-ceph/rook-ceph-operator');
});

test('merge command is copyable in the review header (not on list cards)', async ({ page }) => {
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
  const clipboard = () => page.evaluate(() => (window as unknown as { __copied: string[] }).__copied);

  // Review header: the command is shown verbatim and copies as-is. (Read the
  // recorder before navigating away — a full document load resets it.)
  await page.goto('/#/pr/142');
  const bar = page.locator('.merge-cmd');
  await expect(bar.locator('.merge-cmd-text')).toHaveText('gh pr merge 142 --repo acme/home-ops');
  // Flag tokens render dimmed/distinct, but the command text — and what copies —
  // is the verbatim command (the flag span only colours, never alters it).
  await expect(bar.locator('.merge-cmd-text .cmd-flag')).toHaveText('--repo');
  // Both the copy button and the chip itself copy the verbatim command.
  await bar.locator('.copy-btn').click();
  expect(await clipboard()).toContain('gh pr merge 142 --repo acme/home-ops');
  await bar.locator('.merge-cmd-text').click();
  expect(await clipboard()).toContain('gh pr merge 142 --repo acme/home-ops');

  // The PR list no longer carries a copy-merge affordance — it lives only in
  // the review header now.
  await page.goto('/');
  await expect(page.locator('.card-actions')).toHaveCount(0);
});

test('a list row expands to a brief diff summary; the row still opens the PR', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');

  const card = page.locator('.card-li:has(.card-shell[data-pr="142"])');
  await expect(card.locator('.card-preview')).toHaveCount(0); // collapsed by default

  // The chevron expands the row and lazy-loads the summary — without navigating.
  await card.locator('.card-expand').click();
  await expect(page).not.toHaveURL(/#\/pr\//);
  const preview = card.locator('.card-preview');
  await expect(preview).toBeVisible();

  // Ordered sections: copy (the merge command, from the list data — same
  // MergeCommand chip as the diff overview), resource diffs, cautions & warnings,
  // image changes.
  await expect(preview.locator('.merge-cmd-text')).toHaveText('gh pr merge 142 --repo acme/home-ops');
  // Flags render as distinct (dimmed) tokens so `142 --repo` doesn't read joined.
  await expect(preview.locator('.merge-cmd-text .cmd-flag')).toHaveText('--repo');
  await expect(preview).toContainText('Resource diffs');
  await expect(preview).toContainText('Cautions');
  await expect(preview).toContainText('Render failures');
  await expect(preview).toContainText('Image changes');
  await expect(preview.locator('.pv-caution')).toHaveCount(2);
  await expect(preview.locator('.pv-caution').first()).toContainText('StatefulSet default/postgres');
  await expect(preview.locator('.pv-image').first()).toContainText('ghcr.io/rook/ceph');
  await expect(preview.locator('.pv-failure')).toContainText('plex');

  // The chevron toggles it shut again.
  await card.locator('.card-expand').click();
  await expect(card.locator('.card-preview')).toHaveCount(0);

  // Clicking the row itself still opens the full review.
  await card.locator('.card').click();
  await expect(page).toHaveURL(/#\/pr\/142$/);
});

test('the row summary waits for its fetch before sliding open (no first-open jump)', async ({ page }) => {
  await stubApi(page);
  // Hold the summary response so we can observe the pre-load state. (Registered
  // after stubApi, so this handler wins for the summary route.)
  let release: () => void = () => {};
  const gate = new Promise<void>((r) => (release = r));
  await page.route('**/api/prs/142/summary', async (route) => {
    await gate;
    await route.fulfill({ json: diffEnvelope });
  });
  await page.goto('/');
  const card = page.locator('.card-li:has(.card-shell[data-pr="142"])');

  await card.locator('.card-expand').click();
  // Toggled open, but the panel is deferred until the summary lands: the chevron
  // shows a spinner and no panel (which would otherwise slide to a loading height
  // and then jump) has mounted yet.
  await expect(card.locator('.card-expand')).toHaveAttribute('aria-expanded', 'true');
  await expect(card.locator('.card-expand .kspin')).toBeVisible();
  await expect(card.locator('.card-preview')).toHaveCount(0);

  // Summary lands → the panel mounts once, with its full content.
  release();
  await expect(card.locator('.card-preview')).toBeVisible();
  await expect(card.locator('.card-preview')).toContainText('StatefulSet default/postgres');
});

test('the expand-all control toggles every row summary at once', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');

  await expect(page.locator('.card-preview')).toHaveCount(0);
  const toggle = page.locator('.expand-all');
  await expect(toggle).toContainText('Expand all');

  // Expand all → every rendered row's summary opens (only #142 here has one).
  await toggle.click();
  await expect(toggle).toContainText('Collapse all');
  await expect(page.locator('.card-preview')).toHaveCount(1);
  await expect(page.locator('.card-preview')).toContainText('StatefulSet default/postgres');

  // Collapse all → back to none.
  await toggle.click();
  await expect(toggle).toContainText('Expand all');
  await expect(page.locator('.card-preview')).toHaveCount(0);
});

test('a scroll-to-top button appears after scrolling and returns to the top', async ({ page }) => {
  // A tall list so .list-screen actually overflows in the test viewport.
  const many = Array.from({ length: 24 }, (_, i) => ({
    number: 200 + i,
    title: `chore: bump dependency number ${200 + i}`,
    author: 'renovate[bot]',
    state: 'open',
    open: true,
    draft: false,
    headRef: `pr/${200 + i}`,
    headSha: 'abc',
    baseRef: 'main',
    createdAt: '2026-06-01T09:00:00Z',
    updatedAt: '2026-06-04T12:00:00Z',
    labels: [],
    url: '#',
    status: 'ready',
    signals: { resources: 1, caution: 0, images: 1, failures: 0 },
  }));
  await page.route('**/api/meta', (r) => r.fulfill({ json: defaultMeta }));
  await page.route('**/api/prs', (r) => r.fulfill({ json: many }));
  await page.routeWebSocket('**/ws', () => {});
  await page.goto('/');
  // 24 PRs paginate (default 10); switch to "All" so the whole list renders and
  // .list-screen overflows enough to exercise the FAB.
  await page.locator('.page-size select').selectOption('all');
  await expect(page.locator('.card')).toHaveCount(24);

  const fab = page.locator('.scroll-top');
  await expect(fab).toBeHidden();

  // Scroll the list down → the button appears.
  await page.locator('.list-screen').evaluate((el) => el.scrollTo({ top: 1500 }));
  await expect(fab).toBeVisible();

  // Clicking it smooth-scrolls back to the top, and the button hides again.
  await fab.click();
  await expect.poll(() => page.locator('.list-screen').evaluate((el) => el.scrollTop)).toBe(0);
  await expect(fab).toBeHidden();
});

test('the review is one scrollable document; scrolling drives the selection', async ({ page }) => {
  await stubApi(page);
  await page.goto('/#/pr/142');

  // Summary + every resource render stacked — reviewing means scrolling, not
  // clicking each resource.
  await expect(page.locator('.diff-section')).toHaveCount(4);
  await expect(page.locator('[data-sel="r1"] .res-title')).toContainText('rook-config');

  // Scrolling to the bottom lands on the last resource: the tree selection and
  // the URL follow (replaceState — no history spam).
  await page.locator('.diff-pane').evaluate((el) => (el.scrollTop = el.scrollHeight));
  await expect(page).toHaveURL(/#\/pr\/142\/r2$/);
  await expect(page.locator('.tree-item.selected')).toContainText('default/postgres');

  // Scrolling back to the top re-selects the Summary.
  await page.locator('.diff-pane').evaluate((el) => (el.scrollTop = 0));
  await expect(page.locator('.tree .tree-summary')).toHaveClass(/selected/);

  // A tree click still jumps: the section scrolls to the pane top.
  await page.locator('.tree .tree-item').nth(1).click();
  await expect(page).toHaveURL(/#\/pr\/142\/r1$/);
  const paneTop = await page.locator('.diff-pane').evaluate((el) => el.getBoundingClientRect().top);
  const secTop = await page
    .locator('[data-sel="r1"]')
    .evaluate((el) => el.getBoundingClientRect().top);
  expect(Math.abs(secTop - paneTop)).toBeLessThanOrEqual(2);
});

test('a warning deep-links to the flagged resource diff', async ({ page }) => {
  await stubApi(page);
  await page.goto('/#/pr/142');

  // The caution's resource rendered into the diff → it's a button that jumps
  // straight to that diff.
  await page.locator('.flag-link.caution').click();
  await expect(page).toHaveURL(/#\/pr\/142\/r2$/);
  await expect(page.locator('[data-sel="r2"] .res-title')).toContainText('StatefulSet default/postgres');

  // The caution also rides along in that resource's sticky header (the global
  // caution strip scrolls away in the stacked view); clean resources carry none.
  const headerBadge = page.locator('[data-sel="r2"] .res-header .badge.caution');
  await expect(headerBadge).toBeVisible();
  await expect(headerBadge).toHaveAttribute('title', /PersistentVolumeClaims/);
  await expect(page.locator('[data-sel="r0"] .res-header .badge')).toHaveCount(0);
});

test('zero counts stay neutral (impact delta) and hidden (diff header)', async ({ page }) => {
  // A diff with nothing added/removed: the "+0 added" / "−0 removed" segments
  // must not carry their green/red tint (colored zeros draw the eye to nothing).
  const diff = { ...diffEnvelope.diff!, summary: { added: 0, changed: 2, removed: 0 } };
  await page.route('**/api/meta', (r) => r.fulfill({ json: defaultMeta }));
  await page.route('**/api/prs', (r) => r.fulfill({ json: samplePRs }));
  await page.route('**/api/prs/142/diff', (r) => r.fulfill({ json: { ...diffEnvelope, diff } }));
  await page.routeWebSocket('**/ws', () => {});
  await page.goto('/#/pr/142');

  // The change delta: +0 added and −0 removed are muted (.zero), the non-zero
  // changed segment keeps its tint.
  await expect(page.locator('.d-seg.add')).toHaveClass(/zero/);
  await expect(page.locator('.d-seg.del')).toHaveClass(/zero/);
  await expect(page.locator('.d-seg.chg')).not.toHaveClass(/zero/);

  // The removed StatefulSet (+0 −5): the header hides the zero, like the tree.
  await page.goto('/#/pr/142/r2');
  await expect(page.locator('[data-sel="r2"] .res-counts .del')).toHaveText('-5');
  await expect(page.locator('[data-sel="r2"] .res-counts .add')).toHaveCount(0);
});

test('a long image list renders in full by default (no collapse)', async ({ page }) => {
  // Image changes live in their own summary column, so even a long list renders
  // open by default — there's no fold-behind-the-count control.
  const many = Array.from({ length: 9 }, (_, i) => ({
    name: `ghcr.io/app-${i}`,
    from: `v1.${i}.0`,
    to: `v1.${i}.1`,
    refs: [`Deployment apps/app-${i}`],
  }));
  const diff = { ...diffEnvelope.diff!, images: many };
  await page.route('**/api/meta', (r) => r.fulfill({ json: defaultMeta }));
  await page.route('**/api/prs', (r) => r.fulfill({ json: samplePRs }));
  await page.route('**/api/prs/142/diff', (r) => r.fulfill({ json: { ...diffEnvelope, diff } }));
  await page.routeWebSocket('**/ws', () => {});
  await page.goto('/#/pr/142');

  await expect(page.locator('.img-change')).toHaveCount(9);
  await expect(page.locator('.ov-head')).toHaveCount(0); // no fold control
});

test('the summary always lays out four columns, with a green placeholder for an empty section', async ({ page }) => {
  // The four columns — render failures, cautions, blast radius, image changes —
  // are always present so the layout holds steady across PRs. This fixture has no
  // render failures, so that column shows a "No render failures" placeholder
  // rather than collapsing.
  const diff = {
    ...diffEnvelope.diff!,
    failures: [],
    warnings: [{ level: 'caution', rule: 'removed-statefulset', resource: 'StatefulSet default/db', detail: 'data may be deleted' }],
    blastRadius: [{ parent: 'Kustomization a/b', direct: ['Kustomization c/d'], transitive: 1 }],
    images: [{ name: 'ghcr.io/app', from: 'v1.0.0', to: 'v1.1.0', refs: null }],
  };
  await page.route('**/api/meta', (r) => r.fulfill({ json: defaultMeta }));
  await page.route('**/api/prs', (r) => r.fulfill({ json: samplePRs }));
  await page.route('**/api/prs/142/diff', (r) => r.fulfill({ json: { ...diffEnvelope, diff } }));
  await page.routeWebSocket('**/ws', () => {});
  await page.goto('/#/pr/142');

  const cols = page.locator('.ov-col');
  await expect(cols).toHaveCount(4);
  await expect(cols.nth(0)).toContainText('Render failures');
  await expect(cols.nth(1)).toContainText('Cautions');
  await expect(cols.nth(2)).toContainText('Blast radius');
  await expect(cols.nth(3)).toContainText('Image changes');

  // The empty render-failures column shows the green placeholder, not a flag.
  await expect(cols.nth(0).locator('.ov-empty')).toHaveText('No render failures');
  await expect(cols.nth(0).locator('.flag')).toHaveCount(0);
});

test('the summary grid resolves to 4 / 2 / 1 columns by pane width (never an orphaned 3+1)', async ({ page }) => {
  // The grid uses explicit container-query breakpoints, not auto-fit, so the four
  // columns always land in a balanced shape: four across when wide, a 2×2 at medium
  // widths, a single stack when narrow. The resolved track count is the assertion —
  // a count of 3 would be the old orphaned "3 + lonely 1" layout.
  await stubApi(page);
  // getComputedStyle resolves grid-template-columns to one px value per track.
  const trackCount = () =>
    page.locator('.ov-grid').evaluate((el) => getComputedStyle(el).gridTemplateColumns.split(' ').length);

  // Wide desktop (rail + a roomy pane): four across.
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto('/#/pr/142');
  await expect(page.locator('.ov-grid')).toBeVisible();
  await expect.poll(trackCount).toBe(4);

  // Ultrawide: the grid fills the pane (no fixed cap leaving dead space on the
  // right). It tracks the pane width — within its 14px side padding of .overview.
  await page.setViewportSize({ width: 2000, height: 900 });
  await expect.poll(trackCount).toBe(4);
  const fill = await page.locator('.ov-grid').evaluate((el) => {
    const pane = (el.closest('.overview') as HTMLElement).clientWidth - 28; // minus 14px padding each side
    return el.getBoundingClientRect().width / pane;
  });
  expect(fill).toBeGreaterThan(0.98); // fills the pane, not capped at 1280

  // Mid width — the pane auto-fit used to orphan (3 + 1). Now a balanced 2×2.
  await page.setViewportSize({ width: 960, height: 900 });
  await expect.poll(trackCount).toBe(2);

  // Mobile (the switcher's Summary pane, full width): a single stack.
  await page.setViewportSize({ width: 390, height: 900 });
  await expect.poll(trackCount).toBe(1);
});

test('an open PR with cautions carries an amber card edge', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');
  // #142 carries a caution signal; #138 doesn't. The merged #128 never does.
  await expect(page.locator('.card-shell[data-pr="142"]')).toHaveClass(/caution/);
  await expect(page.locator('.card-shell[data-pr="138"]')).not.toHaveClass(/caution/);
});

test('keyboard help: ? toggles the overlay, Esc closes it, / focuses the filter', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');

  // '?' opens the shortcuts overlay; Escape closes it (without navigating).
  await page.locator('body').press('?');
  await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeVisible();
  await page.locator('body').press('Escape');
  await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toHaveCount(0);

  // The topbar button is the discoverable entry point.
  await page.getByRole('button', { name: 'Keyboard shortcuts' }).click();
  await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeVisible();
  // Click the backdrop's corner — its centre sits under the help card.
  await page.locator('.help-backdrop').click({ position: { x: 8, y: 8 } });
  await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toHaveCount(0);

  // '/' focuses the filter box on the list; typed keys then go to the filter.
  await page.locator('body').press('/');
  await expect(page.getByPlaceholder('Filter pull requests…')).toBeFocused();
});

test('deep link to a missing resource lands on the Summary', async ({ page }) => {
  await stubApi(page);
  // The diff has r0/r1/r2; rZZZ is gone → the URL is corrected to the PR root
  // and the Summary is shown rather than an empty, broken selection.
  await page.goto('/#/pr/142/rZZZ');
  await expect(page).toHaveURL(/#\/pr\/142$/);
  await expect(page.locator('[data-sel="summary"] .impact')).toBeVisible();
});

test('palette returns focus to its trigger on close', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');
  const search = page.getByRole('button', { name: 'Search pull requests' });
  await search.click();
  const dialog = page.getByRole('dialog', { name: 'Search pull requests' });
  await expect(dialog.getByRole('textbox')).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  await expect(search).toBeFocused(); // focus returns, not dropped to <body>
});

test('help dialog: focus return, Tab trap, and Ctrl+K swaps to the palette', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');
  const helpBtn = page.getByRole('button', { name: 'Keyboard shortcuts' });
  const help = page.getByRole('dialog', { name: 'Keyboard shortcuts' });

  // Open from the button → Esc → focus returns to the button.
  await helpBtn.click();
  await expect(help).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(help).toHaveCount(0);
  await expect(helpBtn).toBeFocused();

  // Tab is trapped — focus stays on the dialog, not the page behind the backdrop.
  await helpBtn.click();
  await page.keyboard.press('Tab');
  await expect(help).toBeFocused();

  // Ctrl+K from the open help closes it and opens the palette (mutually exclusive).
  await page.keyboard.press('Control+k');
  await expect(help).toHaveCount(0);
  await expect(page.getByRole('dialog', { name: 'Search pull requests' })).toBeVisible();
});

test('list footer: project links, version, license, year — and no topbar GitHub button', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');

  const footer = page.locator('.footer');
  await expect(footer.getByRole('link', { name: /konflate v1\.2\.3/ })).toHaveAttribute(
    'href',
    'https://github.com/home-operations/konflate',
  );
  await expect(footer.getByRole('link', { name: 'Discord' })).toHaveAttribute(
    'href',
    'https://discord.gg/home-operations',
  );
  await expect(footer.getByRole('link', { name: 'AGPL-3.0' })).toHaveAttribute('href', /LICENSE/);
  // The © glyph is now an icon from the library (labeled for screen readers),
  // alongside the year and owner.
  await expect(footer.getByRole('img', { name: 'Copyright' })).toBeVisible();
  await expect(footer).toContainText(`${new Date().getFullYear()} home-operations`);

  // The project GitHub link moved out of the topbar into the footer.
  await expect(page.locator('.actions a')).toHaveCount(0);

  // The review screen stays footer-free (its panes own the space).
  await page.locator('.card-shell[data-pr="142"]').click();
  await expect(page.locator('.impact')).toBeVisible();
  await expect(page.locator('.footer')).toHaveCount(0);
});

test('an empty PR list reads as the success state, not an error', async ({ page }) => {
  await page.route('**/api/meta', (r) => r.fulfill({ json: defaultMeta }));
  await page.route('**/api/prs', (r) => r.fulfill({ json: [] }));
  await page.routeWebSocket('**/ws', () => {});
  await page.goto('/');
  await expect(page.locator('.all-clear')).toContainText('All caught up');
  // A healthy poll shows no sync banner — the empty list is genuine, not a failure.
  await expect(page.locator('.sync-banner')).toHaveCount(0);
});

test('a rate-limited forge poll shows a banner with a reset countdown, not a bare empty list', async ({ page }) => {
  // The misread that motivated this: an anonymous (rate-limited) poll returned no
  // PRs and the UI read as "all caught up". Now meta carries sync.ok=false and the
  // banner explains why — with the reset time and how to raise the limit.
  const retryAt = Math.floor(Date.now() / 1000) + 11 * 60;
  await page.route('**/api/meta', (r) =>
    r.fulfill({
      json: {
        ...defaultMeta,
        sync: { ok: false, reason: 'rate_limited', message: 'GitHub API rate limit exceeded.', retryAt },
      },
    }),
  );
  await page.route('**/api/prs', (r) => r.fulfill({ json: [] }));
  await page.routeWebSocket('**/ws', () => {});
  await page.goto('/');

  const banner = page.locator('.sync-banner');
  await expect(banner).toBeVisible();
  await expect(banner).toHaveCSS('justify-content', 'center'); // content is centred, not left-hugged
  await expect(banner).toContainText('GitHub API rate limit exceeded.');
  await expect(banner).toContainText(/Resets in ~\d+ minutes?\./);
  await expect(banner).toContainText('Configure a forge token or GitHub App');
});

test('a generic poll failure banners the error but keeps any loaded PRs', async ({ page }) => {
  // Unlike a rate limit, an unreachable forge has no reset time or token remedy —
  // the banner just names the failure and reassures that the list below is stale,
  // not empty. (Pushed live as a "sync" websocket event here.)
  await stubApi(page);
  let pushSync: ((data: string) => void) | null = null;
  await page.routeWebSocket('**/ws', (ws) => {
    pushSync = (data) => ws.send(data);
  });
  await page.goto('/');
  await expect(page.locator('.card')).toHaveCount(3);
  await expect(page.locator('.sync-banner')).toHaveCount(0); // healthy to start

  await expect.poll(() => pushSync !== null).toBe(true);
  pushSync!(JSON.stringify({ type: 'sync', sync: { ok: false, reason: 'error', message: 'forge unreachable' } }));

  const banner = page.locator('.sync-banner');
  await expect(banner).toBeVisible();
  await expect(banner).toContainText('forge unreachable');
  await expect(banner).toContainText('still shown below');
  await expect(banner).not.toContainText('Resets in');
  await expect(page.locator('.card')).toHaveCount(3); // the kept PRs remain

  // A recovering "sync" event (ok=true) clears the banner.
  pushSync!(JSON.stringify({ type: 'sync', sync: { ok: true } }));
  await expect(page.locator('.sync-banner')).toHaveCount(0);
});

test('mobile: back and prev/next share one header row, title below (compact chrome)', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await stubApi(page);
  await page.goto('/#/pr/142');
  await page.locator('.impact').waitFor();

  const back = await page.locator('.review-head > .btn-icon').boundingBox();
  const next = await page.getByRole('button', { name: 'Next PR' }).boundingBox();
  const title = await page.locator('.review-title').boundingBox();
  // One row of buttons (same y), with the title block below it — previously the
  // back button, title, and nav stacked into three rows of chrome.
  expect(Math.abs((back?.y ?? 0) - (next?.y ?? 99))).toBeLessThanOrEqual(1);
  expect(title?.y ?? 0).toBeGreaterThan((back?.y ?? 0) + (back?.height ?? 0) - 1);
  // And the search/shortcuts buttons are gone from the topbar (no keyboard on touch).
  await expect(page.locator('.kbd-btn:visible')).toHaveCount(0);
});

test('mobile: the PR header scrolls away and the switcher stays pinned', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await stubApi(page);
  await page.goto('/#/pr/142');
  await page.locator('.impact').waitFor();

  // On mobile the whole review column scrolls (not the inner diff pane), so the
  // header can scroll away and the diff gets the full viewport. The switcher is
  // the one bar pinned throughout.
  const review = page.locator('.review');
  const head = page.locator('.review-head');
  const switcher = page.locator('.diff-switcher');
  await expect(switcher).toBeVisible();
  const headYBefore = (await head.boundingBox())?.y ?? 0;

  await review.evaluate((el) => (el.scrollTop = el.scrollHeight));

  // The scrollspy is wired to .review (not the now-static pane), so scrolling
  // still drives the selection to the last resource…
  await expect(page).toHaveURL(/#\/pr\/142\/r2$/);
  // …the switcher stayed pinned at the top…
  await expect(switcher).toBeVisible();
  // …and the header scrolled up out of the way.
  const headYAfter = (await head.boundingBox())?.y ?? 0;
  expect(headYAfter).toBeLessThan(headYBefore);
});

test('captures list + overview screenshots (light)', async ({ page }) => {
  await stubApi(page);
  await page.addInitScript(() => localStorage.setItem('konflate-theme', 'light'));
  await page.goto('/');
  await expect(page.locator('.card')).toHaveCount(3);
  await page.screenshot({ path: 'screenshots/konflate-list.png' });

  await page.locator('.card-shell[data-pr="142"]').click();
  await expect(page.locator('.impact')).toBeVisible();
  await page.screenshot({ path: 'screenshots/konflate-overview.png' });
});

test('captures diffs screenshot (dark, deep-linked)', async ({ page }) => {
  await stubApi(page);
  await page.addInitScript(() => localStorage.setItem('konflate-theme', 'dark'));
  await page.goto('/#/pr/142/r0');
  await expect(page.locator('table.diff tr.row-add').first()).toBeVisible();
  await expect(page.locator('html.dark')).toBeAttached();
  await page.screenshot({ path: 'screenshots/konflate-diffs-dark.png' });
});

test('review chrome: rail Summary and section headers form one level bar (regression #163)', async ({ page }) => {
  await stubApi(page);
  await page.goto('/#/pr/142');
  await page.locator('.res-header').first().waitFor();

  // The rail's Summary node and the diff pane's summary header sit flush at
  // the same top (no blank band above the Summary) and share one height, so
  // their bottom borders form a single continuous line across the rail border.
  const box = (sel: string) =>
    page.locator(sel).first().evaluate((el) => {
      const b = el.getBoundingClientRect();
      return { top: b.top, bottom: b.bottom };
    });
  const tree = await box('.tree-summary');
  const head = await box('.diff-section[data-sel="summary"] .res-header');
  expect(Math.abs(tree.top - head.top)).toBeLessThanOrEqual(0.5);
  expect(Math.abs(tree.bottom - head.bottom)).toBeLessThanOrEqual(0.5);

  // A stuck section header must overlap the scrollport's top edge (top <= pane
  // top) so fractional scroll offsets can't open a hairline of the content
  // scrolling beneath it ("text inside the bar", issue #163).
  await page.evaluate(() => {
    const pane = document.querySelector('.diff-pane') as HTMLElement;
    pane.scrollTop = 150.25; // inside the summary section's body
  });
  await page.waitForTimeout(100);
  const stuck = await page.evaluate(() => {
    const pane = document.querySelector('.diff-pane') as HTMLElement;
    const h = document.querySelector('.diff-section[data-sel="summary"] .res-header')!;
    return h.getBoundingClientRect().top - pane.getBoundingClientRect().top;
  });
  expect(stuck).toBeLessThanOrEqual(0);
});

test("'/' finds text across lazy-mounted diff sections and jumps to hits", async ({ page }) => {
  await stubApi(page);
  await page.goto('/#/pr/142');
  await page.locator('.res-header').first().waitFor();

  // '/' opens the find bar with the input focused.
  await page.keyboard.press('/');
  const bar = page.locator('.diff-search');
  await expect(bar).toBeVisible();
  await expect(bar.locator('input')).toBeFocused();

  // Typing computes the hit count live (searching the diff DATA — the browser's
  // Ctrl+F can't see lazy-mounted sections) without jumping yet.
  await bar.locator('input').fill('apiVersion');
  await expect(bar.locator('.search-count')).toHaveText('2 hits');
  await expect(page).toHaveURL(/#\/pr\/142$/);

  // Enter steps through the hits, navigating to each hit's resource and
  // flashing the matched row.
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#\/pr\/142\/r0$/);
  await expect(bar.locator('.search-count')).toHaveText('1/2');
  await expect(page.locator('[data-sel="r0"] tr.search-hit')).toBeVisible();
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#\/pr\/142\/r1$/);
  await expect(bar.locator('.search-count')).toHaveText('2/2');

  // A term that lives only in the LAST resource's diff still hits and jumps —
  // the whole point versus Ctrl+F.
  await bar.locator('input').fill('statefulset');
  await expect(bar.locator('.search-count')).toHaveText('1 hit');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#\/pr\/142\/r2$/);
  await expect(page.locator('[data-sel="r2"] tr.search-hit')).toBeVisible();

  // Escape closes the bar; a second Escape (outside the input) leaves the
  // review as before.
  await page.keyboard.press('Escape');
  await expect(bar).toHaveCount(0);
  await page.keyboard.press('Escape');
  await expect(page).toHaveURL(/#\/$|\/$/);
});
