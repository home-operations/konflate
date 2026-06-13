// PR-list page size: how many rows the list shows per page. A personal display
// preference (not a shareable view coordinate, so it lives in localStorage rather
// than the URL — the page *number* is the deep-linkable part, in the router). The
// page number itself resets on a size change, since the windowing changes.

export type PageSize = 10 | 20 | 50 | 'all';

// The default, and the smallest, page size: the list's pager appears once a list
// outgrows it, and any unknown stored/selected value falls back to it. Typed as
// the literal (via satisfies) so it stays usable in numeric comparisons.
export const DEFAULT_PAGE_SIZE = 10 satisfies PageSize;

// Offered in the size picker, smallest first; 'all' is the escape hatch back to
// the un-paginated list (so Ctrl-F still finds every row).
export const PAGE_SIZES: readonly PageSize[] = [DEFAULT_PAGE_SIZE, 20, 50, 'all'];

const KEY = 'konflate-pagesize';

// parsePageSize resolves a stored or <select>-supplied string to a known PageSize,
// falling back to the default — the single home for "string → PageSize", shared by
// the persisted load and the size picker.
export function parsePageSize(v: string | null): PageSize {
  return PAGE_SIZES.find((s) => String(s) === v) ?? DEFAULT_PAGE_SIZE;
}

// Guarded so importing this module never throws outside a browser (tests, a
// future SSR build); there it falls back to the default until the UI runs.
function load(): PageSize {
  if (typeof localStorage === 'undefined') return DEFAULT_PAGE_SIZE;
  return parsePageSize(localStorage.getItem(KEY));
}

export const paging = $state<{ size: PageSize }>({ size: load() });

export function setPageSize(size: PageSize): void {
  paging.size = size;
  if (typeof localStorage !== 'undefined') localStorage.setItem(KEY, String(size));
}
