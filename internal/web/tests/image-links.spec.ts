import { test, expect, type Page } from '@playwright/test';
import { samplePRs } from './fixtures';
import type { Meta, DiffEnvelope } from '../src/lib/types';

const meta: Meta = { forge: 'github', repo: 'acme/home-ops', refreshIntervalSeconds: 1800 };

function image(name: string) {
  return { name, from: 'v1.0', to: 'v1.1', refs: [] };
}

// One image per registry mapping the Overview can derive, plus one it can't.
const envelope: DiffEnvelope = {
  status: 'ready',
  pr: samplePRs[0],
  diff: {
    prNumber: 142,
    headSha: 'abc',
    summary: { added: 0, changed: 0, removed: 0 },
    impact: { resources: 0, parents: 0, namespaces: [], crds: 0 },
    images: [
      image('ghcr.io/rook/ceph'), // ghcr → github repo
      image('ghcr.io/thelounge/thelounge:4.5.0'), // tag on the name is stripped
      image('quay.io/prometheus/prometheus'), // quay → quay/repository
      image('nginx'), // bare → Docker Hub library
      image('grafana/grafana'), // org/repo → Docker Hub r/
      image('registry.k8s.io/coredns/coredns'), // no derivable web UI → plain text
    ],
    failures: [],
    warnings: [],
    chromaCss: '',
    tree: [],
    resources: [],
  },
  mergeCommand: '',
};

async function stubApi(page: Page) {
  await page.route('**/api/meta', (r) => r.fulfill({ json: meta }));
  await page.route('**/api/prs', (r) => r.fulfill({ json: samplePRs }));
  await page.route('**/api/prs/142/diff', (r) => r.fulfill({ json: envelope }));
  await page.routeWebSocket('**/ws', () => {
    /* accept */
  });
}

test('image names link to their registry page (and stay plain when undecidable)', async ({ page }) => {
  await stubApi(page);
  await page.goto('/#/pr/142');

  // The summary section (Overview) renders the image list.
  await expect(page.locator('.img-list')).toBeVisible();

  const href = (name: string) =>
    page.locator('.img-change', { hasText: name }).locator('a.img-name-link');

  await expect(href('ghcr.io/rook/ceph')).toHaveAttribute('href', 'https://github.com/rook/ceph');
  // A digest-pinned image carries its tag on the name; the link drops it.
  await expect(href('ghcr.io/thelounge/thelounge')).toHaveAttribute(
    'href',
    'https://github.com/thelounge/thelounge',
  );
  await expect(href('quay.io/prometheus/prometheus')).toHaveAttribute(
    'href',
    'https://quay.io/repository/prometheus/prometheus',
  );
  await expect(href('nginx')).toHaveAttribute('href', 'https://hub.docker.com/_/nginx');
  await expect(href('grafana/grafana')).toHaveAttribute('href', 'https://hub.docker.com/r/grafana/grafana');

  // Links open in a new tab and don't leak the opener.
  await expect(href('ghcr.io/rook/ceph')).toHaveAttribute('target', '_blank');
  await expect(href('ghcr.io/rook/ceph')).toHaveAttribute('rel', /noopener/);

  // A registry with no derivable web UI stays plain text — no link.
  const coredns = page.locator('.img-change', { hasText: 'registry.k8s.io/coredns/coredns' });
  await expect(coredns.locator('a.img-name-link')).toHaveCount(0);
  await expect(coredns.locator('.img-name')).toBeVisible();
});
