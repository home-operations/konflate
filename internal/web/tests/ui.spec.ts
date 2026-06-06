import { test, expect, type Page } from '@playwright/test';
import { samplePRs, diffEnvelope } from './fixtures';
import type { Meta } from '../src/lib/types';

const defaultMeta: Meta = {
  forge: 'github',
  repo: 'acme/home-ops',
  repoUrl: 'https://github.com/acme/home-ops',
  version: '1.2.3',
  refreshIntervalSeconds: 1800,
};

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

test('list → review → single-page flow', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');

  // Topbar shows the forge logo + repo (linked to its forge page) and the
  // auto-update indicator — there is no manual refresh button.
  await expect(page.locator('.repo')).toContainText('acme/home-ops');
  await expect(page.locator('.repo svg[role="img"]')).toBeVisible();
  await expect(page.locator('a.repo')).toHaveAttribute('href', 'https://github.com/acme/home-ops');
  await expect(page.locator('a.repo')).toHaveAttribute('target', '_blank');
  await expect(page.locator('.actions .auto')).toContainText('30m');
  await expect(page.locator('.actions .btn', { hasText: 'Refresh' })).toHaveCount(0);

  // Landing list: cards with per-PR signal badges.
  await expect(page.locator('.card')).toHaveCount(3);
  const card142 = page.locator('.card', { hasText: '#142' });
  // The PR number lives in the meta row (behind a PR glyph), so the title owns
  // its own line and carries no "#142".
  await expect(card142.locator('.pr-id')).toContainText('#142');
  await expect(card142.locator('.card-title')).toHaveText(
    'feat(rook-ceph): bump the rook-ceph operator and cluster chart to v1.15.0',
  );
  await expect(card142.locator('.badge.danger').first()).toBeVisible();
  await expect(card142.locator('.ago').first()).toHaveText(/ago|just now/); // humanized timestamps
  // Author avatar renders when present; a PR without one falls back to the icon.
  await expect(card142.locator('img.avatar')).toBeVisible();
  await expect(page.locator('.card', { hasText: '#138' }).locator('img.avatar')).toHaveCount(0);
  // PR age ("opened …") and a colored label dot.
  await expect(card142.locator('.ago', { hasText: 'opened' })).toBeVisible();
  await expect(card142.locator('.label-dot')).toBeVisible();

  // Open a PR → the single-page review lands on the Summary (impact, warnings,
  // image changes, render failures), with the tree rail alongside it.
  await card142.click();
  await expect(page).toHaveURL(/#\/pr\/142$/);
  await expect(page.locator('.danger-strip')).toContainText('danger');
  await expect(page.locator('.impact')).toContainText('resources');
  await expect(page.locator('.warning.danger')).toContainText('StatefulSet');
  await expect(page.locator('.img-list')).toContainText('ghcr.io/rook/ceph');
  await expect(page.locator('.failure')).toContainText('plex');
  // The tree: a Summary node (selected by default) + one leaf per changed
  // resource. The danger warning surfaces a marker on the Summary node.
  await expect(page.locator('.tree .tree-summary')).toHaveClass(/selected/);
  await expect(page.locator('.tree-summary .summary-danger')).toBeVisible();
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
  const card = page.locator('.card', { hasText: '#142' });
  await expect(card.locator('.badge[title*="refresh"]')).toBeVisible();
  await expect(card.locator('.badge.danger').first()).toBeVisible();

  // The review shows a banner and still renders the kept diff (tree intact).
  await card.click();
  await expect(page.locator('.refresh-strip')).toContainText("Couldn't refresh");
  await expect(page.locator('.tree .tree-item')).toHaveCount(3);
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
  // No redundant "merged" tag — the group header, dimmed card, purple dot, and
  // "merged …" timestamp already convey it.
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

  const running = page.locator('.card', { hasText: 'busy pr' });
  await expect(running.locator('.card-status.running .kspin')).toBeVisible(); // thematic wheel spinner
  await expect(running).toContainText('rendering');
  await expect(page.locator('.card', { hasText: 'waiting pr' })).toContainText('queued');
});

test('the PR list loading state shows the smasher, then the list arrives', async ({ page }) => {
  await page.route('**/api/meta', (r) => r.fulfill({ json: defaultMeta }));
  await page.route('**/api/prs', async (r) => {
    await new Promise((res) => setTimeout(res, 800)); // hold the list briefly
    await r.fulfill({ json: samplePRs });
  });
  await page.routeWebSocket('**/ws', () => {});
  await page.goto('/');

  await expect(page.locator('.list-screen .smasher')).toBeVisible();
  await expect(page.locator('.loading-center')).toContainText('Loading pull requests');
  await expect(page.locator('.card')).toHaveCount(3); // and the data still lands
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
  // The percussive-maintenance mascot, with its parts present and animated.
  await expect(page.locator('.loading-center .smasher')).toBeVisible();
  await expect(page.locator('.smasher .smash-arm')).toBeAttached();
  await expect(page.locator('.loading-center')).toContainText('Rendering');
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
  await stubApi(page, { forge: 'forgejo', repo: 'me/home-ops', refreshIntervalSeconds: 600 });
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
  await expect(page.locator('.card')).toContainText('#131');
});

test('the filter understands facet tokens (status:/author:/base:/label:)', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');
  const input = page.locator('.pr-search');

  await input.fill('status:danger');
  await expect(page.locator('.card')).toHaveCount(1);
  await expect(page.locator('.card')).toContainText('#142');

  // Tokens AND together, and with free text.
  await input.fill('author:octocat base:staging');
  await expect(page.locator('.card')).toHaveCount(1);
  await expect(page.locator('.card')).toContainText('#138');

  await input.fill('label:media plex');
  await expect(page.locator('.card')).toHaveCount(1);
  await expect(page.locator('.card')).toContainText('#131');

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

  // Risk floats first with no query: the danger PR leads the group.
  await expect(dialog.locator('.palette-row .row-title').first()).toContainText('rook-ceph');
  // Signal preview rides on the row.
  await expect(dialog.locator('.palette-row').first().locator('.badge.danger').first()).toBeVisible();

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
  const ids = page.locator('.cards .pr-id');
  await expect(ids.nth(0)).toContainText('#138');
  await expect(ids.nth(1)).toContainText('#142');
  await expect(ids.nth(2)).toContainText('#131');

  // Refreshed sort: last render desc → #142 (12:00) > #138 (11:30) > #131 (10:00).
  await page.locator('.sort select').selectOption('refreshed');
  await expect(ids.nth(0)).toContainText('#142');
  await expect(ids.nth(1)).toContainText('#138');

  // The danger pill narrows to the one PR carrying danger warnings; clicking
  // again clears it.
  const danger = page.locator('.sum-pill', { hasText: 'danger' });
  await danger.click();
  await expect(danger).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('.card')).toHaveCount(1);
  await expect(page.locator('.card')).toContainText('#142');
  await danger.click();
  await expect(page.locator('.card')).toHaveCount(3);

  // failed → the render-error PR.
  await page.locator('.sum-pill', { hasText: 'failed' }).click();
  await expect(page.locator('.card')).toHaveCount(1);
  await expect(page.locator('.card')).toContainText('#131');

  // merged → only the merged shelf, auto-expanded; counts stay visible.
  await page.locator('.sum-pill', { hasText: 'merged' }).click();
  await expect(page.locator('.merged-cards .card')).toHaveCount(1);
  await expect(page.locator('.card')).toHaveCount(1);
  await expect(page.locator('.list-summary')).toContainText('3 open');

  // Status filter + a text query that excludes everything → the no-match state.
  await page.getByPlaceholder('Filter pull requests…').fill('plex');
  await expect(page.locator('.card')).toHaveCount(0);
  await expect(page.locator('.empty')).toContainText('match your filter');
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

test('mobile: a long PR title wraps to two lines instead of truncating', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await stubApi(page);
  await page.goto('/');
  const title = page.locator('.card', { hasText: '#142' }).locator('.card-title');
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

test('merge command is copyable in the review header and on list cards', async ({ page }) => {
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
  await expect(bar.locator('code')).toHaveText('gh pr merge 142 --repo acme/home-ops');
  await bar.locator('.copy-btn').click();
  expect(await clipboard()).toContain('gh pr merge 142 --repo acme/home-ops');

  // List: one copy affordance per open PR (the merged card carries none).
  await page.goto('/');
  await expect(page.locator('.card-actions')).toHaveCount(3);
  const card = page.locator('.card-li', { hasText: '#142' });
  await card.hover();
  await card.locator('.card-actions .copy-btn').click();
  expect(await clipboard()).toContain('gh pr merge 142 --repo acme/home-ops');
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

  // The danger warning's resource rendered into the diff → it's a button that
  // jumps straight to that diff.
  await page.locator('.warning.danger').click();
  await expect(page).toHaveURL(/#\/pr\/142\/r2$/);
  await expect(page.locator('[data-sel="r2"] .res-title')).toContainText('StatefulSet default/postgres');

  // The warning also rides along in that resource's sticky header (the global
  // danger strip scrolls away in the stacked view); clean resources carry none.
  const headerBadge = page.locator('[data-sel="r2"] .res-header .badge.danger');
  await expect(headerBadge).toBeVisible();
  await expect(headerBadge).toHaveAttribute('title', /PersistentVolumeClaims/);
  await expect(page.locator('[data-sel="r0"] .res-header .badge')).toHaveCount(0);
});

test('zero counts stay neutral (impact pills) and hidden (diff header)', async ({ page }) => {
  // A diff with nothing added/removed: the "+0 added" / "−0 removed" pills must
  // not carry their green/red tint (colored zeros draw the eye to nothing).
  const diff = { ...diffEnvelope.diff!, summary: { added: 0, changed: 2, removed: 0 } };
  await page.route('**/api/meta', (r) => r.fulfill({ json: defaultMeta }));
  await page.route('**/api/prs', (r) => r.fulfill({ json: samplePRs }));
  await page.route('**/api/prs/142/diff', (r) => r.fulfill({ json: { ...diffEnvelope, diff } }));
  await page.routeWebSocket('**/ws', () => {});
  await page.goto('/#/pr/142');

  await expect(page.locator('.impact-pill', { hasText: 'added' })).not.toHaveClass(/add/);
  await expect(page.locator('.impact-pill', { hasText: 'removed' })).not.toHaveClass(/del/);
  await expect(page.locator('.impact-pill', { hasText: 'changed' })).toHaveClass(/chg/);

  // The removed StatefulSet (+0 −5): the header hides the zero, like the tree.
  await page.goto('/#/pr/142/r2');
  await expect(page.locator('[data-sel="r2"] .res-counts .del')).toHaveText('-5');
  await expect(page.locator('[data-sel="r2"] .res-counts .add')).toHaveCount(0);
});

test('an open PR with danger warnings carries a red card edge', async ({ page }) => {
  await stubApi(page);
  await page.goto('/');
  // #142 carries a danger signal; #138 doesn't. The merged #128 never does.
  await expect(page.locator('.card', { hasText: '#142' })).toHaveClass(/danger/);
  await expect(page.locator('.card', { hasText: '#138' })).not.toHaveClass(/danger/);
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
  await expect(footer).toContainText(`© ${new Date().getFullYear()} home-operations`);

  // The project GitHub link moved out of the topbar into the footer.
  await expect(page.locator('.actions a')).toHaveCount(0);

  // The review screen stays footer-free (its panes own the space).
  await page.locator('.card', { hasText: '#142' }).click();
  await expect(page.locator('.impact')).toBeVisible();
  await expect(page.locator('.footer')).toHaveCount(0);
});

test('an empty PR list reads as the success state, not an error', async ({ page }) => {
  await page.route('**/api/meta', (r) => r.fulfill({ json: defaultMeta }));
  await page.route('**/api/prs', (r) => r.fulfill({ json: [] }));
  await page.routeWebSocket('**/ws', () => {});
  await page.goto('/');
  await expect(page.locator('.all-clear')).toContainText('All caught up');
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
  await page.goto('/#/pr/142/r0');
  await expect(page.locator('table.diff tr.row-add').first()).toBeVisible();
  await expect(page.locator('html.dark')).toBeAttached();
  await page.screenshot({ path: 'screenshots/konflate-diffs-dark.png' });
});
