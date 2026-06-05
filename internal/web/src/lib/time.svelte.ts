// A shared reactive clock so relative timestamps ("5m ago") stay current
// without each component owning a timer. Components that read `clock.now`
// re-render when it ticks.

export const clock = $state({ now: Date.now() });

export function initClock(): void {
  setInterval(() => {
    clock.now = Date.now();
  }, 30_000);
}

// timeAgo renders an ISO timestamp as a compact relative string against `now`
// (pass clock.now so it updates live). Empty/invalid input yields "".
export function timeAgo(iso: string | undefined, now: number): string {
  if (!iso) return '';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 45) return 'just now';
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.round(h / 24);
  if (d < 30) return `${d}d ago`;
  const mo = Math.round(d / 30);
  if (mo < 12) return `${mo}mo ago`;
  return `${Math.round(mo / 12)}y ago`;
}

// absolute renders the full local timestamp for a tooltip.
export function absolute(iso: string | undefined): string {
  if (!iso) return '';
  const t = Date.parse(iso);
  return Number.isNaN(t) ? '' : new Date(t).toLocaleString();
}
