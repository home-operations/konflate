// In-review diff search. The diff tables are lazy-mounted (only sections near
// the viewport hold a real <table>) and content-visibility skips off-screen
// rendering, so the browser's Ctrl+F cannot find text in sections you haven't
// scrolled to. This searches the diff *data* instead — every resource's rows,
// mounted or not — and jumps to the match: navigating to the resource mounts
// its table, then the matching row is scrolled into view and flashed.
import { router } from './router.svelte';
import { store, openSel } from './store.svelte';

// One searchable diff line: the resource it lives in plus the line numbers the
// rendered rows carry as data attributes (data-old / data-new in both the
// unified and split tables), so a hit can be located in whichever view mode is
// active.
interface Entry {
  res: string;
  oldNo?: number;
  newNo?: number;
  text: string;
}

export const search = $state({
  open: false,
  q: '',
  hits: [] as Entry[],
  // cur indexes hits once a jump happened; -1 = typed but not yet stepped.
  cur: -1,
});

// The flat text index, rebuilt only when the diff identity changes. Folded
// context rows are skipped — they are collapsed unchanged lines; the diff's
// own add/del/ctx rows are what review search is for.
let index: Entry[] = [];
let indexKey = '';

function buildIndex(): void {
  const d = store.diff;
  const key = d ? `${d.prNumber}@${d.headSha}` : '';
  if (key === indexKey) return;
  indexKey = key;
  index = [];
  const parser = new DOMParser();
  for (const res of d?.resources ?? []) {
    for (const row of res.unified) {
      if (row.hunk || row.folded || !row.html) continue;
      const text = parser.parseFromString(row.html, 'text/html').body.textContent ?? '';
      if (text.trim() === '') continue;
      index.push({ res: res.id, oldNo: row.oldNo, newNo: row.newNo, text });
    }
  }
}

export function openSearch(): void {
  buildIndex();
  search.open = true;
}

export function closeSearch(): void {
  search.open = false;
  search.q = '';
  search.hits = [];
  search.cur = -1;
}

// setQuery recomputes the hit list live (the bar shows the count); stepping is
// what jumps. Sub-2-char queries match nothing — one letter hits every line.
export function setQuery(q: string): void {
  search.q = q;
  search.cur = -1;
  const needle = q.trim().toLowerCase();
  if (needle.length < 2) {
    search.hits = [];
    return;
  }
  search.hits = index.filter((e) => e.text.toLowerCase().includes(needle));
}

// step advances through the hits (dir ±1) and jumps to the new current one.
// The first step after a query lands on the first hit.
export function step(dir: 1 | -1): void {
  const n = search.hits.length;
  if (n === 0) return;
  search.cur = search.cur < 0 ? (dir === 1 ? 0 : n - 1) : (search.cur + dir + n) % n;
  jumpTo(search.hits[search.cur]);
}

// jumpTo navigates to the hit's resource section (the existing navigation
// mounts the lazy table and scrolls the section in), then locates the matching
// row by its line-number data attributes — retrying across frames because the
// table mounts a tick after the route changes — and centres + flashes it.
function jumpTo(hit: Entry): void {
  const r = router.route;
  if (r.name !== 'review') return;
  openSel(r.pr, hit.res);

  const selector =
    `[data-sel="${CSS.escape(hit.res)}"] ` +
    (hit.newNo ? `tr[data-new="${hit.newNo}"]` : `tr[data-old="${hit.oldNo}"]`);
  const deadline = performance.now() + 1500;
  const locate = (): void => {
    const row = document.querySelector<HTMLElement>(selector);
    if (!row) {
      if (performance.now() < deadline) requestAnimationFrame(locate);
      return;
    }
    row.scrollIntoView({ block: 'center' });
    row.classList.add('search-hit');
    setTimeout(() => row.classList.remove('search-hit'), 1600);
  };
  requestAnimationFrame(locate);
}
