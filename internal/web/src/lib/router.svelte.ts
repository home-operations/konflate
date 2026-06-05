// A tiny hash router so reviews are deep-linkable and survive refresh / browser
// back. Routes:
//   #/                       the PR list
//   #/pr/142                 review, Overview tab
//   #/pr/142/diffs           review, Diffs tab
//   #/pr/142/diffs/r0        review, Diffs tab, resource r0 selected

export type Tab = 'overview' | 'diffs';

export type Route =
  | { name: 'list' }
  | { name: 'review'; pr: number; tab: Tab; resource: string | null };

function parse(hash: string): Route {
  const parts = hash.replace(/^#\/?/, '').split('/').filter(Boolean);
  if (parts[0] === 'pr' && parts[1]) {
    const pr = Number(parts[1]);
    if (Number.isInteger(pr)) {
      const tab: Tab = parts[2] === 'diffs' ? 'diffs' : 'overview';
      const resource = tab === 'diffs' ? (parts[3] ?? null) : null;
      return { name: 'review', pr, tab, resource };
    }
  }
  return { name: 'list' };
}

function toHash(r: Route): string {
  if (r.name === 'list') return '#/';
  let h = `#/pr/${r.pr}`;
  if (r.tab === 'diffs') {
    h += '/diffs';
    if (r.resource) h += `/${r.resource}`;
  }
  return h;
}

export const router = $state<{ route: Route }>({ route: parse(location.hash) });

export function navigate(to: Route): void {
  const next = toHash(to);
  if (location.hash === next) {
    router.route = to; // same hash (e.g. resource within diffs) — sync anyway
  } else {
    location.hash = next; // triggers hashchange → updates router.route
  }
}

export function initRouter(): void {
  window.addEventListener('hashchange', () => {
    router.route = parse(location.hash);
  });
}
