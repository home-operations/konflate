// Application data + navigation. The router owns "which PR/resource is shown";
// this store owns the data (instance meta, the PR list, the currently-loaded
// diff) and the actions that mutate it.

import type {
  DiffEnvelope,
  DiffResult,
  DiffSummary,
  ImageChange,
  Impact,
  Meta,
  PRStatus,
  RenderFailure,
  SyncStatus,
  Warning,
  WSEvent,
} from './types';
import { router, navigate } from './router.svelte';

// The status facets a summary pill can filter the list down to ('' = unfiltered;
// 'open' narrows to just the open set, hiding the merged shelf).
export type StatusFilter = '' | 'open' | 'caution' | 'failure' | 'routine' | 'merged' | 'hidden';
// List sort: the field to order by, and the direction. The comparator is
// defined ascending (name A→Z, time oldest-first); 'desc' reverses it.
export type SortKey = 'created' | 'refreshed' | 'name';
export type SortDir = 'asc' | 'desc';

// A lazily-loaded, compact summary of a PR's diff for the list-row expander —
// just the headline facts (impact, cautions, image bumps, failures), never the
// rendered resource HTML. Keyed by PR number; headSha lets a re-render of the
// same PR invalidate a stale entry on the next expand.
export interface RowPreview {
  state: 'loading' | 'ready' | 'pending' | 'error';
  headSha: string;
  error?: string;
  summary?: DiffSummary;
  impact?: Impact;
  warnings?: Warning[];
  images?: ImageChange[];
  failures?: RenderFailure[];
  truncated?: number;
}

interface Store {
  meta: Meta | null;
  prs: PRStatus[];
  loaded: boolean; // the initial PR list has been fetched at least once
  query: string;
  statusFilter: StatusFilter;
  sort: SortKey;
  sortDir: SortDir;
  diff: DiffResult | null;
  diffFor: number | null; // PR number the diff/loading belongs to
  diffError: string;
  diffRefreshError: string; // last re-render of the shown diff failed (last-good kept)
  diffMergeCommand: string; // "copy to merge" command for the shown PR ('' when off/merged)
  diffHidden: boolean; // the shown PR is excluded by the filter — listed but never rendered
  loading: boolean; // a diff fetch/render is in flight
  previews: Record<number, RowPreview>; // lazy list-row diff summaries, by PR number
  connected: boolean;
  sync: SyncStatus | null; // forge-polling health; non-null ⇒ last poll failed (show the banner)
}

export const store: Store = $state({
  meta: null,
  prs: [],
  loaded: false,
  query: '',
  statusFilter: '',
  sort: 'created',
  sortDir: 'desc',
  diff: null,
  diffFor: null,
  diffError: '',
  diffRefreshError: '',
  diffMergeCommand: '',
  diffHidden: false,
  loading: false,
  previews: {},
  connected: false,
  sync: null,
});

// ---- derived helpers ------------------------------------------------------

// Lookups derived once from the loaded diff, so the tree rail, the overview's
// warnings and every resource header don't each re-scan the warning/resource
// lists (the diff can carry dozens of each). Recomputes only when the diff
// changes; readers stay reactive through the shared $derived. Svelte forbids
// exporting a derived directly, so callers read it through diffIndex().
const diffIndexState = $derived.by(() => {
  const warningsByResource = new Map<string, Warning[]>();
  const cautionResources = new Set<string>(); // resource titles carrying a caution
  let cautionCount = 0; // total cautions (a resource may have several)
  for (const w of store.diff?.warnings ?? []) {
    let list = warningsByResource.get(w.resource);
    if (!list) warningsByResource.set(w.resource, (list = []));
    list.push(w);
    cautionResources.add(w.resource);
    cautionCount++;
  }
  // Resource id keyed by its "Kind ns/name" title, so a warning can deep-link to
  // the diff it flags without a linear find.
  const idByTitle = new Map<string, string>();
  for (const r of store.diff?.resources ?? []) idByTitle.set(r.title, r.id);
  return { warningsByResource, cautionResources, cautionCount, idByTitle };
});

// diffIndex exposes the shared diff lookups (see diffIndexState). Read it inside
// a component's reactive context ($derived/template) to stay live.
export function diffIndex() {
  return diffIndexState;
}

// ---- query grammar ----------------------------------------------------
// A query is free text plus optional facet tokens, AND-ed together:
//   "plex status:caution author:renovate base:main label:storage"
// The same grammar drives the inline filter and the command palette.

export interface ParsedQuery {
  tokens: { key: string; value: string }[];
  text: string;
}

const FACETS = ['status', 'author', 'base', 'label'];
// Every queryable status:<v> value — i.e. StatusFilter minus the unfiltered ''.
// `satisfies readonly StatusFilter[]` makes the compiler reject a typo or a value
// that isn't a real StatusFilter, so this list can't silently drift out of sync
// with matchesStatus again. ('hidden' was missing here, so `status:hidden` typed
// in the filter or palette matched nothing — the pill worked only because it sets
// statusFilter directly, bypassing this grammar.)
const STATUS_VALUES = ['open', 'caution', 'failure', 'routine', 'merged', 'hidden'] as const satisfies readonly StatusFilter[];

export function parseQuery(raw: string): ParsedQuery {
  const tokens: ParsedQuery['tokens'] = [];
  const text: string[] = [];
  for (const piece of raw.trim().split(/\s+/).filter(Boolean)) {
    const m = piece.match(/^([a-z]+):(.+)$/i);
    if (m && FACETS.includes(m[1].toLowerCase())) {
      tokens.push({ key: m[1].toLowerCase(), value: m[2].toLowerCase() });
    } else {
      text.push(piece.toLowerCase());
    }
  }
  return { tokens, text: text.join(' ') };
}

export function matchesQuery(p: PRStatus, q: ParsedQuery): boolean {
  for (const t of q.tokens) {
    switch (t.key) {
      case 'status': {
        // Prefix-match the canonical names so "status:cau" works. want is already
        // a StatusFilter (STATUS_VALUES is typed), so no cast is needed.
        const want = STATUS_VALUES.find((v) => v.startsWith(t.value));
        if (!want || !matchesStatus(p, want)) return false;
        break;
      }
      case 'author':
        if (!(p.author ?? '').toLowerCase().includes(t.value)) return false;
        break;
      case 'base':
        if (!(p.baseRef ?? '').toLowerCase().includes(t.value)) return false;
        break;
      case 'label':
        if (!(p.labels ?? []).some((l) => l.name.toLowerCase().includes(t.value))) return false;
        break;
    }
  }
  if (q.text) {
    const hay = [
      p.title,
      String(p.number),
      p.author ?? '',
      p.baseRef ?? '',
      (p.labels ?? []).map((l) => l.name).join(' '),
    ]
      .join(' ')
      .toLowerCase();
    if (!hay.includes(q.text)) return false;
  }
  return true;
}

export function filteredPRs(): PRStatus[] {
  const raw = store.query.trim();
  if (!raw) return store.prs;
  const q = parseQuery(raw);
  return store.prs.filter((p) => matchesQuery(p, q));
}

// statusFromQuery resolves a status: facet in the raw query to its StatusFilter,
// or null when the query carries none. The List uses it so a typed
// `status:hidden` / `status:merged` selects that section like the matching pill
// would; without it the default pill ('' = open, non-hidden) re-hides exactly the
// PRs the query asked for. Prefix-matched like matchesQuery, so `status:mer`
// resolves too; the first status token wins if several are present.
export function statusFromQuery(raw: string): StatusFilter | null {
  for (const t of parseQuery(raw).tokens) {
    if (t.key === 'status') {
      const want = STATUS_VALUES.find((v) => v.startsWith(t.value));
      if (want) return want;
    }
  }
  return null;
}

// matchesStatus is the per-PR predicate for a summary-pill filter.
export function matchesStatus(p: PRStatus, f: StatusFilter): boolean {
  switch (f) {
    case 'caution':
      return p.open && !p.hidden && (p.signals?.caution ?? 0) > 0;
    case 'failure':
      return p.open && !p.hidden && (p.signals?.failures ?? 0) > 0;
    case 'routine':
      // Only image/chart-version fields changed, nothing flagged — the fast-merge
      // pile. A diff-shape signal, not a runtime guarantee (see the server's
      // DiffResult.Routine).
      return p.open && !p.hidden && !!p.signals?.routine;
    case 'merged':
      return !p.open;
    case 'hidden':
      return !!p.hidden; // excluded by the PR filter — listed but never rendered
    // '' (the default view) and 'open' both show the open, non-hidden set.
    default:
      return p.open && !p.hidden;
  }
}

// An unparsable/missing timestamp sorts last under the desc time orders.
const ts = (iso?: string): number => {
  const v = iso ? Date.parse(iso) : NaN;
  return Number.isNaN(v) ? 0 : v;
};

// sortPRs orders a list by the selected key and direction. The comparator is
// ascending (name A→Z, time oldest-first); 'desc' (the default) reverses it, so
// created/refreshed read newest-first and name Z→A. Returns a copy — the store's
// array stays in server order.
export function sortPRs(list: PRStatus[]): PRStatus[] {
  const { sort: key, sortDir } = store;
  const dir = sortDir === 'asc' ? 1 : -1;
  return [...list].sort((a, b) => {
    const asc =
      key === 'name'
        ? a.title.localeCompare(b.title)
        : key === 'refreshed'
          ? ts(a.updatedAt) - ts(b.updatedAt)
          : ts(a.createdAt) - ts(b.createdAt);
    return dir * asc;
  });
}

// visiblePRs is the exact list the List view renders: the text-query-filtered
// set, narrowed by the active status pill (or a `status:` facet in the query),
// in the selected sort order. It is the single source of truth for "what's on
// screen" — the List reads it, and adjacentPR walks it so the review's prev/next
// navigation steps through PRs in the same order and subset the user sees.
export function visiblePRs(): PRStatus[] {
  const effective = statusFromQuery(store.query) ?? store.statusFilter;
  return sortPRs(filteredPRs().filter((p) => matchesStatus(p, effective)));
}

export function currentPR(): PRStatus | null {
  const r = router.route;
  if (r.name !== 'review') return null;
  return store.prs.find((p) => p.number === r.pr) ?? null;
}

// ---- navigation -----------------------------------------------------------

export function openPR(n: number): void {
  navigate({ name: 'review', pr: n, sel: null });
}

// Select a tree node in a review: 'summary', a resource id, or null (a bare
// #/pr/N, which the review renders as the Summary).
export function openSel(n: number, sel: string | null = null): void {
  navigate({ name: 'review', pr: n, sel });
}

export function goList(): void {
  navigate({ name: 'list' });
}

// The ordered selection cycle for j/k and the mobile switcher: Summary first,
// then every resource.
export function selectables(): string[] {
  return ['summary', ...(store.diff?.resources ?? []).map((res) => res.id)];
}

export function adjacentPR(delta: number): void {
  const r = router.route;
  if (r.name !== 'review') return;
  // Walk the on-screen list (filtered + sorted), not the raw server order, so
  // prev/next lands on the visually adjacent PR and never a filtered-out one.
  const list = visiblePRs();
  const i = list.findIndex((p) => p.number === r.pr);
  const j = i + delta;
  if (i >= 0 && j >= 0 && j < list.length) openPR(list[j].number);
}

// Move the selection by delta through [Summary, ...resources], clamped at the
// ends. A null selection (the default landing) sits on Summary, so j from there
// steps into the first resource.
export function adjacentResource(delta: number): void {
  const r = router.route;
  if (r.name !== 'review') return;
  const ids = selectables();
  const i = Math.max(0, ids.indexOf(r.sel ?? 'summary'));
  const j = Math.min(Math.max(i + delta, 0), ids.length - 1);
  openSel(r.pr, ids[j]);
}

// ---- API ------------------------------------------------------------------

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Accept: 'application/json' } });
  if (!res.ok && res.status !== 202) throw new Error(`${path}: HTTP ${res.status}`);
  return (await res.json()) as T;
}

export async function loadMeta(): Promise<void> {
  try {
    store.meta = await getJSON<Meta>('/api/meta');
    store.sync = store.meta.sync ?? null; // seed the banner from the initial poll health
  } catch (err) {
    console.error('loadMeta', err);
  }
}

export async function loadPRs(): Promise<void> {
  try {
    store.prs = await getJSON<PRStatus[]>('/api/prs');
  } catch (err) {
    console.error('loadPRs', err);
  } finally {
    store.loaded = true;
  }
}

// settleLoading applies the loading state from a resolved fetch: a still-
// rendering PR keeps `loading` set (the review shows a text status message,
// never a spinner); a ready or errored one clears it.
function settleLoading(rendering: boolean): void {
  store.loading = rendering;
}

// ensureDiff loads PR n's diff if it isn't already the current one. Called by
// the App whenever the route points at a review.
export function ensureDiff(n: number): void {
  if (store.diffFor === n) return;
  store.diffFor = n;
  store.diff = null;
  store.diffError = '';
  store.diffRefreshError = ''; // clear the previous PR's refresh error until this one's envelope lands
  store.diffMergeCommand = '';
  store.diffHidden = false;
  store.loading = true;
  void loadDiff(n);
}

async function loadDiff(n: number): Promise<void> {
  try {
    const env = await getJSON<DiffEnvelope>(`/api/prs/${n}/diff`);
    if (store.diffFor !== n) return; // route moved on
    applyEnvelope(env);
  } catch (err) {
    if (store.diffFor === n) {
      settleLoading(false);
      store.diffError = String(err);
    }
  }
}

// ensurePreview lazily loads the compact diff summary behind a list row's
// expander. Cached per PR and keyed by headSha, so it fetches once per render —
// a re-render (new sha) refetches on the next expand. Errors are cached too (no
// auto-retry loop); reopen the PR for the live view.
export function ensurePreview(n: number, headSha: string): void {
  const cur = store.previews[n];
  // A settled (ready/error) preview for this sha is served from cache; a still-
  // 'pending' one is refetched — the render may have finished since it was first
  // opened, and this call (a user re-expand) is the retrigger.
  if (cur && cur.headSha === headSha && cur.state !== 'pending') return;
  store.previews[n] = { state: 'loading', headSha };
  void loadPreview(n, headSha);
}

async function loadPreview(n: number, headSha: string): Promise<void> {
  try {
    // The lean summary endpoint: the headline facts without the per-resource
    // render, so a row preview doesn't pull the whole diff payload.
    const env = await getJSON<DiffEnvelope>(`/api/prs/${n}/summary`);
    if (store.previews[n]?.headSha !== headSha) return; // a newer expand superseded this
    if (env.status === 'ready' && env.diff) {
      const d = env.diff;
      store.previews[n] = {
        state: 'ready',
        headSha,
        summary: d.summary,
        impact: d.impact,
        warnings: d.warnings ?? [],
        images: d.images ?? [],
        failures: d.failures ?? [],
        truncated: d.truncated,
      };
    } else if (env.status === 'error') {
      store.previews[n] = { state: 'error', headSha, error: env.error ?? 'render failed' };
    } else {
      store.previews[n] = { state: 'pending', headSha };
    }
  } catch (err) {
    if (store.previews[n]?.headSha === headSha) {
      store.previews[n] = { state: 'error', headSha, error: String(err) };
    }
  }
}

function applyEnvelope(env: DiffEnvelope): void {
  store.diffHidden = env.hidden ?? false; // excluded by the filter → the review shows a notice, not a spinner
  // Set on every envelope (the command depends only on the PR's open state, not
  // the render), so a reviewer can copy it while the diff is still rendering.
  store.diffMergeCommand = env.mergeCommand ?? '';
  settleLoading(env.status === 'pending' || env.status === 'running');
  if (env.status === 'ready' && env.diff) {
    store.diff = env.diff;
    injectChroma(env.diff.chromaCss);
    store.diffError = '';
    store.diffRefreshError = env.refreshError ?? '';
  } else if (env.status === 'error') {
    store.diffError = env.error ?? 'render failed';
  }
}

// ---- chroma stylesheet -----------------------------------------------------

// injectChroma keeps a single <style id="chroma-css"> in sync with the CSS the
// server ships alongside each diff. All diffs currently share one theme, but
// updating on change (rather than injecting once) means a server-side theme
// change never silently keeps the stale stylesheet.
function injectChroma(css: string): void {
  if (!css) return;
  let style = document.getElementById('chroma-css');
  if (!style) {
    style = document.createElement('style');
    style.id = 'chroma-css';
    document.head.append(style);
  }
  if (style.textContent !== css) style.textContent = css;
}

// ---- websocket ------------------------------------------------------------

let wsAttempt = 0;

export function connectWS(): void {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${proto}://${location.host}/ws`);
  ws.addEventListener('open', () => {
    store.connected = true;
    // A reconnect (not the first connect) means we were disconnected: any events
    // that fired during the gap were missed, so re-pull the authoritative list,
    // meta, and the open diff rather than trusting the now-stale local state.
    if (wsAttempt > 0) {
      void loadMeta();
      void loadPRs();
      if (store.diffFor !== null) void loadDiff(store.diffFor);
    }
    wsAttempt = 0; // reset the backoff once we're connected
  });
  ws.addEventListener('message', (e) => {
    let ev: WSEvent;
    try {
      ev = JSON.parse(e.data as string) as WSEvent;
    } catch {
      return;
    }
    onEvent(ev);
  });
  ws.addEventListener('close', () => {
    store.connected = false;
    // Exponential backoff with jitter: every client otherwise reconnects in
    // lockstep ~2s after a server restart (a thundering herd on the new pod).
    // delay ∈ [base/2, base), base = 1s·2^attempt capped at 30s.
    const base = Math.min(30_000, 1_000 * 2 ** wsAttempt);
    wsAttempt++;
    setTimeout(connectWS, base / 2 + Math.random() * (base / 2));
  });
  ws.addEventListener('error', () => ws.close());
}

function onEvent(ev: WSEvent): void {
  // A PR that is no longer open is dropped from the list (server reconciled to
  // the forge's open set on a full refresh).
  if (ev.type === 'removed') {
    store.prs = store.prs.filter((p) => p.number !== ev.number);
    return;
  }
  // A CI rollup changed (poll or status webhook): update just that PR's checks,
  // clearing them when the new state is none (so the indicator disappears).
  if (ev.type === 'checks') {
    const pr = store.prs.find((p) => p.number === ev.number);
    if (pr) pr.checks = ev.checks.state ? ev.checks : undefined;
    return;
  }
  // Forge-polling health changed: raise the banner (ok=false) or clear it (ok=true).
  if (ev.type === 'sync') {
    store.sync = ev.sync.ok ? null : ev.sync;
    return;
  }
  // ev is narrowed to the 'status' variant here — status is guaranteed present.
  const pr = store.prs.find((p) => p.number === ev.number);
  if (pr) {
    pr.status = ev.status;
    pr.error = ev.error;
  }
  // The list endpoint carries the signal summary; re-pull it so badges update.
  if (ev.status === 'ready' || ev.status === 'error') {
    void loadPRs();
    if (ev.number === store.diffFor) void loadDiff(ev.number);
    // A row whose preview was opened mid-render is cached 'pending'; now that it
    // settled, refresh it in place so an open panel updates without a re-expand.
    const pv = store.previews[ev.number];
    if (pv && pv.state === 'pending') void loadPreview(ev.number, pv.headSha);
  }
}
