// A tiny hash router so reviews are deep-linkable and survive refresh / browser
// back. Routes:
//   #/                       the PR list
//   #/pr/142                 review, default selection (the Summary)
//   #/pr/142/summary         review, Summary panel (explicit)
//   #/pr/142/r0              review, resource r0's diff
//
// `sel` is the selected tree node: 'summary', a resource id, or null. A bare
// #/pr/142 (null) renders the Summary — the first tree node — so the default
// URL stays clean.

export type Route =
  | { name: 'list' }
  | { name: 'review'; pr: number; sel: string | null };

function parse(hash: string): Route {
  const parts = hash.replace(/^#\/?/, '').split('/').filter(Boolean);
  if (parts[0] === 'pr' && parts[1]) {
    const pr = Number(parts[1]);
    if (Number.isInteger(pr)) {
      return { name: 'review', pr, sel: parts[2] ?? null };
    }
  }
  return { name: 'list' };
}

function toHash(r: Route): string {
  if (r.name === 'list') return '#/';
  return r.sel ? `#/pr/${r.pr}/${r.sel}` : `#/pr/${r.pr}`;
}

export const router = $state<{ route: Route }>({ route: parse(location.hash) });

export function navigate(to: Route): void {
  const next = toHash(to);
  if (location.hash === next) {
    router.route = to; // same hash (e.g. re-selecting the current node) — sync anyway
  } else {
    location.hash = next; // triggers hashchange → updates router.route
  }
}

export function initRouter(): void {
  window.addEventListener('hashchange', () => {
    router.route = parse(location.hash);
  });
}
