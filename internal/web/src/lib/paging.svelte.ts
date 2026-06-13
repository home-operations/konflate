// PR-list page size: how many rows the list shows per page. A personal display
// preference (not a shareable view coordinate, so it lives in localStorage rather
// than the URL — the page *number* is the deep-linkable part, in the router). The
// page number itself resets on a size change, since the windowing changes.

export type PageSize = 10 | 20 | 50 | 'all';

// Offered in the size picker, smallest first; 'all' is the escape hatch back to
// the un-paginated list (so Ctrl-F still finds every row).
export const PAGE_SIZES: readonly PageSize[] = [10, 20, 50, 'all'];

const KEY = 'konflate-pagesize';

// Guarded so importing this module never throws outside a browser (tests, a
// future SSR build); there it falls back to the default until the UI runs.
function load(): PageSize {
  if (typeof localStorage === 'undefined') return 10;
  const v = localStorage.getItem(KEY);
  if (v === 'all') return 'all';
  const n = Number(v);
  return n === 10 || n === 20 || n === 50 ? n : 10;
}

export const paging = $state<{ size: PageSize }>({ size: load() });

export function setPageSize(size: PageSize): void {
  paging.size = size;
  if (typeof localStorage !== 'undefined') localStorage.setItem(KEY, String(size));
}
