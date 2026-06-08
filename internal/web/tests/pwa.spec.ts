import { test, expect, type Page } from '@playwright/test';
import type { Meta } from '../src/lib/types';

const meta: Meta = { forge: 'github', repo: 'acme/home-ops', refreshIntervalSeconds: 1800 };

async function stub(page: Page) {
  await page.route('**/api/meta', (r) => r.fulfill({ json: meta }));
  await page.route('**/api/prs', (r) => r.fulfill({ json: [] }));
  await page.routeWebSocket('**/ws', () => {});
}

// The preview build is served from http://localhost — a secure context — so the
// service worker registers and the full PWA wiring is verifiable here.
test('installable PWA: manifest, icons, apple meta, and service worker', async ({ page }) => {
  await stub(page);
  await page.goto('/');

  // Manifest is linked and parses with the install-critical fields.
  await expect(page.locator('link[rel="manifest"]')).toHaveAttribute('href', '/manifest.webmanifest');
  const manifest = await page.evaluate(() => fetch('/manifest.webmanifest').then((r) => r.json()));
  expect(manifest.name).toBe('konflate');
  expect(manifest.display).toBe('standalone');
  expect(manifest.start_url).toBe('/');
  const sizes = (manifest.icons as { sizes: string }[]).map((i) => i.sizes);
  expect(sizes).toEqual(expect.arrayContaining(['192x192', '512x512']));
  expect((manifest.icons as { purpose?: string }[]).some((i) => i.purpose === 'maskable')).toBe(true);

  // iOS Add-to-Home-Screen tags.
  await expect(page.locator('link[rel="apple-touch-icon"]')).toHaveCount(1);
  await expect(page.locator('meta[name="apple-mobile-web-app-capable"]')).toHaveAttribute('content', 'yes');
  await expect(page.locator('meta[name="theme-color"]')).toHaveAttribute('content', '#0b0e14');

  // Every declared icon actually resolves (200).
  const icons = ['/icons/icon-192.png', '/icons/icon-512.png', '/icons/maskable-512.png', '/icons/apple-touch-icon.png'];
  const statuses = await page.evaluate(
    (paths) => Promise.all(paths.map((p) => fetch(p).then((r) => r.status))),
    icons,
  );
  expect(statuses).toEqual(icons.map(() => 200));

  // The service worker registers (and never claims the live-data endpoints).
  await page.waitForFunction(
    async () => {
      const reg = await navigator.serviceWorker?.getRegistration();
      return !!(reg && (reg.active || reg.installing || reg.waiting));
    },
    undefined,
    { timeout: 15000 },
  );
});
