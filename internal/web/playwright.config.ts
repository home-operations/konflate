import { defineConfig, devices } from '@playwright/test';

// Headless-Chromium UI test. The webServer builds the app and serves the real
// dist via `vite preview`; the spec stubs the konflate API with fixtures, so
// the test is deterministic and needs no backend, forge, or network.
export default defineConfig({
  testDir: './tests',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  // Retry in CI so a transient flake (a dropped keypress, a slow mount) doesn't
  // fail the whole job: Playwright reruns only the failed test and marks it
  // "flaky" if it then passes, so chronic failures still surface. This also
  // activates the trace below — 'on-first-retry' is a no-op without retries.
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI ? 'github' : 'list',
  use: {
    baseURL: 'http://localhost:4173',
    trace: 'on-first-retry',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: 'npm run build && npm run preview -- --port 4173 --strictPort',
    port: 4173,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
