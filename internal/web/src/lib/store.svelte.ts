// Application data + navigation. The router owns "which PR/resource is shown";
// this store owns the data (instance meta, the PR list, the currently-loaded
// diff) and the actions that mutate it.

import type { DiffEnvelope, DiffResult, Meta, PRStatus, WSEvent } from './types';
import { router, navigate } from './router.svelte';

// The status facets a summary pill can filter the list down to ('' = all).
export type StatusFilter = '' | 'danger' | 'failed' | 'rendering' | 'merged';
// List sort orders: newest first, recently refreshed first, or by title.
export type SortKey = 'created' | 'refreshed' | 'name';

interface Store {
  meta: Meta | null;
  prs: PRStatus[];
  loaded: boolean; // the initial PR list has been fetched at least once
  query: string;
  statusFilter: StatusFilter;
  sort: SortKey;
  diff: DiffResult | null;
  diffFor: number | null; // PR number the diff/loading belongs to
  diffError: string;
  diffRefreshError: string; // last re-render of the shown diff failed (last-good kept)
  diffMergeCommand: string; // "copy to merge" command for the shown PR ('' when off/merged)
  loading: boolean;
  connected: boolean;
}

export const store: Store = $state({
  meta: null,
  prs: [],
  loaded: false,
  query: '',
  statusFilter: '',
  sort: 'created',
  diff: null,
  diffFor: null,
  diffError: '',
  diffRefreshError: '',
  diffMergeCommand: '',
  loading: false,
  connected: false,
});

// ---- derived helpers ------------------------------------------------------

export function filteredPRs(): PRStatus[] {
  const q = store.query.trim().toLowerCase();
  if (!q) return store.prs;
  return store.prs.filter(
    (p) =>
      p.title.toLowerCase().includes(q) ||
      String(p.number).includes(q) ||
      (p.author ?? '').toLowerCase().includes(q),
  );
}

// matchesStatus is the per-PR predicate for a summary-pill filter.
export function matchesStatus(p: PRStatus, f: StatusFilter): boolean {
  switch (f) {
    case 'danger':
      return (p.signals?.danger ?? 0) > 0;
    case 'failed':
      return p.status === 'error';
    case 'rendering':
      return p.status === 'pending' || p.status === 'running';
    case 'merged':
      return !p.open;
    default:
      return true;
  }
}

// An unparsable/missing timestamp sorts last under the desc time orders.
const ts = (iso?: string): number => {
  const v = iso ? Date.parse(iso) : NaN;
  return Number.isNaN(v) ? 0 : v;
};

// sortPRs orders a list by the selected sort: created/refreshed newest first,
// name A→Z. Returns a copy — the store's array stays in server order.
export function sortPRs(list: PRStatus[]): PRStatus[] {
  const key = store.sort;
  return [...list].sort((a, b) => {
    if (key === 'name') return a.title.localeCompare(b.title);
    if (key === 'refreshed') return ts(b.updatedAt) - ts(a.updatedAt);
    return ts(b.createdAt) - ts(a.createdAt);
  });
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
  const i = store.prs.findIndex((p) => p.number === r.pr);
  const j = i + delta;
  if (i >= 0 && j >= 0 && j < store.prs.length) openPR(store.prs[j].number);
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

// ensureDiff loads PR n's diff if it isn't already the current one. Called by
// the App whenever the route points at a review.
export function ensureDiff(n: number): void {
  if (store.diffFor === n) return;
  store.diffFor = n;
  store.diff = null;
  store.diffError = '';
  store.diffMergeCommand = '';
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
      store.loading = false;
      store.diffError = String(err);
    }
  }
}

function applyEnvelope(env: DiffEnvelope): void {
  // Set on every envelope (the command depends only on the PR's open state, not
  // the render), so a reviewer can copy it while the diff is still rendering.
  store.diffMergeCommand = env.mergeCommand ?? '';
  store.loading = env.status === 'pending' || env.status === 'running';
  if (env.status === 'ready' && env.diff) {
    store.diff = env.diff;
    injectChroma(env.diff.chromaCss);
    store.diffError = '';
    store.diffRefreshError = env.refreshError ?? '';
  } else if (env.status === 'error') {
    store.diffError = env.error ?? 'render failed';
  }
}

// ---- chroma stylesheet (injected once) ------------------------------------

let chromaInjected = false;
function injectChroma(css: string): void {
  if (chromaInjected || !css) return;
  const style = document.createElement('style');
  style.id = 'chroma-css';
  style.textContent = css;
  document.head.append(style);
  chromaInjected = true;
}

// ---- websocket ------------------------------------------------------------

let wsAttempt = 0;

export function connectWS(): void {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${proto}://${location.host}/ws`);
  ws.addEventListener('open', () => {
    store.connected = true;
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
  }
}
