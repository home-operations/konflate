// A tiny hash router so reviews are deep-linkable and survive refresh / browser
// back. Routes:
//   #/                       the PR list (page 1)
//   #/page/2                 the PR list, page 2 (page 1 stays the bare #/)
//   #/pr/142                 review, default selection (the Summary)
//   #/pr/142/summary         review, Summary panel (explicit)
//   #/pr/142/r0              review, resource r0's diff
//
// `sel` is the selected tree node: 'summary', a resource id, or null. A bare
// #/pr/142 (null) renders the Summary — the first tree node — so the default
// URL stays clean. `page` is the list's 1-based page; omitted (page 1) keeps the
// list URL clean too, so a deep link only carries a page once you've paged past
// the first.

export type Route =
  | { name: 'list'; page?: number }
  | { name: 'review'; pr: number; sel: string | null };

function parse(hash: string): Route {
  const parts = hash.replace(/^#\/?/, '').split('/').filter(Boolean);
  if (parts[0] === 'pr' && parts[1]) {
    const pr = Number(parts[1]);
    // PR numbers are positive integers on every forge — anything else is a
    // malformed deep link and falls through to the list.
    if (Number.isInteger(pr) && pr > 0) {
      return { name: 'review', pr, sel: parts[2] ?? null };
    }
  }
  // #/page/N deep-links a list page. N≥2 only — page 1 is the canonical bare #/,
  // so a stray #/page/1 (or a non-integer) normalizes back to it.
  if (parts[0] === 'page' && parts[1]) {
    const page = Number(parts[1]);
    if (Number.isInteger(page) && page > 1) {
      return { name: 'list', page };
    }
  }
  return { name: 'list' };
}

function toHash(r: Route): string {
  if (r.name === 'list') return r.page && r.page > 1 ? `#/page/${r.page}` : '#/';
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

// replace updates the route without a history entry — for scroll-driven
// selection, where every wheel tick would otherwise pollute the back button.
// replaceState fires no hashchange, so the store is updated directly.
export function replace(to: Route): void {
  router.route = to;
  try {
    history.replaceState(null, '', toHash(to));
  } catch {
    // Safari rate-limits replaceState (SecurityError past ~100 calls/30s).
    // The in-memory route is already updated; the URL syncs on the next call.
  }
}

export function initRouter(): void {
  window.addEventListener('hashchange', () => {
    router.route = parse(location.hash);
  });
}
