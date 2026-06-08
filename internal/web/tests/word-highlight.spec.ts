import { test, expect, type Page } from '@playwright/test';
import { samplePRs } from './fixtures';
import type { Meta, DiffEnvelope, DiffResource } from '../src/lib/types';

// Regression guard for intraline word-highlighting on resources OTHER than the
// first. The shared fixture (fixtures.ts) only gives the first resource a `.wd`
// span — its later resources are pure add/remove with no intraline edit — so the
// existing word-highlight test in ui.spec.ts can't catch a highlight that breaks
// only on the second-and-later files in the stacked diff view. This spec ships
// its own diff with TWO `changed` resources, each carrying a `.wd`, and asserts
// the highlight renders identically on both — in unified AND split.

const meta: Meta = { forge: 'github', repo: 'acme/home-ops', refreshIntervalSeconds: 1800 };

// A keyword token + colon, matching how the server wraps a YAML key.
const k = (s: string) => `<span class="nt">${s}</span><span class="p">:</span> `;

// A `changed` resource whose one edited line wraps the changed token in `.wd`,
// in both the unified and split row sets (the two views share the same spans).
function changedRes(id: string, name: string, oldV: string, newV: string): DiffResource {
  const del = `      ${k('image')}<span class="s">repo:v1.<span class="wd">${oldV}</span></span>`;
  const add = `      ${k('image')}<span class="s">repo:v1.<span class="wd">${newV}</span></span>`;
  return {
    id,
    title: `Deployment ns/${name}`,
    kind: 'Deployment',
    name: `ns/${name}`,
    parent: 'HelmRelease ns/app',
    status: 'changed',
    add: 1,
    del: 1,
    unified: [
      { kind: 'ctx', oldNo: 1, newNo: 1, html: `${k('kind')}<span class="s">Deployment</span>` },
      { kind: 'del', oldNo: 2, html: del },
      { kind: 'add', newNo: 2, html: add },
    ],
    side: [{ left: { kind: 'del', no: 2, html: del }, right: { kind: 'add', no: 2, html: add } }],
  };
}

const envelope: DiffEnvelope = {
  status: 'ready',
  pr: samplePRs[0],
  diff: {
    prNumber: 142,
    headSha: 'abc',
    summary: { added: 0, changed: 2, removed: 0 },
    impact: { resources: 2, parents: 1, namespaces: ['ns'], crds: 0 },
    images: [],
    failures: [],
    warnings: [],
    chromaCss: '',
    tree: [
      {
        label: 'HelmRelease ns/app',
        kinds: [
          {
            kind: 'Deployment',
            items: [
              { id: 'r0', name: 'ns/first', status: 'changed', add: 1, del: 1 },
              { id: 'r1', name: 'ns/second', status: 'changed', add: 1, del: 1 },
            ],
          },
        ],
      },
    ],
    resources: [changedRes('r0', 'first', '14.9', '15.0'), changedRes('r1', 'second', '22.1', '22.2')],
  },
  mergeCommand: '',
};

async function stubApi(page: Page) {
  await page.route('**/api/meta', (r) => r.fulfill({ json: meta }));
  await page.route('**/api/prs', (r) => r.fulfill({ json: samplePRs }));
  await page.route('**/api/prs/142/diff', (r) => r.fulfill({ json: envelope }));
  await page.routeWebSocket('**/ws', () => {
    /* accept; no live events needed */
  });
}

const bgOf = (loc: ReturnType<Page['locator']>) =>
  loc.evaluate((el) => getComputedStyle(el).backgroundColor);

test('word highlight renders on a later resource, not just the first (unified)', async ({ page }) => {
  await stubApi(page);
  await page.goto('/#/pr/142');

  // Scroll the second section in so the lazy-mount observer builds its table.
  await page.locator('[data-sel="r1"]').scrollIntoViewIfNeeded();

  const wd0 = page.locator('[data-sel="r0"] table.diff.unified tr.row-add .wd');
  const wd1 = page.locator('[data-sel="r1"] table.diff.unified tr.row-add .wd');
  await expect(wd0).toHaveText('15.0');
  await expect(wd1).toHaveText('22.2');

  // The tint comes from `.row-add .wd`; a later resource must get the same,
  // non-transparent highlight as the first.
  const [bg0, bg1] = [await bgOf(wd0), await bgOf(wd1)];
  expect(bg0).not.toBe('rgba(0, 0, 0, 0)');
  expect(bg1, "the later resource's word highlight must match the first").toBe(bg0);
});

test('word highlight renders on a later resource, not just the first (split)', async ({ page }) => {
  await stubApi(page);
  await page.setViewportSize({ width: 1600, height: 900 }); // ≥1400px defaults to split
  await page.goto('/#/pr/142');

  await page.locator('[data-sel="r1"]').scrollIntoViewIfNeeded();

  const wd0 = page.locator('[data-sel="r0"] table.diff.split td.row-add .wd');
  const wd1 = page.locator('[data-sel="r1"] table.diff.split td.row-add .wd');
  await expect(wd0).toHaveText('15.0');
  await expect(wd1).toHaveText('22.2');

  const [bg0, bg1] = [await bgOf(wd0), await bgOf(wd1)];
  expect(bg0).not.toBe('rgba(0, 0, 0, 0)');
  expect(bg1, "the later resource's word highlight must match the first").toBe(bg0);
});
