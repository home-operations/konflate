// Application data + navigation. The router owns "which PR/resource is shown";
// this store owns the data (instance meta, the PR list, the currently-loaded
// diff) and the actions that mutate it.

import type { DiffEnvelope, DiffResource, DiffResult, Meta, PRStatus, WSEvent } from './types';
import { router, navigate, type Tab } from './router.svelte';

interface Store {
  meta: Meta | null;
  prs: PRStatus[];
  loaded: boolean; // the initial PR list has been fetched at least once
  query: string;
  diff: DiffResult | null;
  diffFor: number | null; // PR number the diff/loading belongs to
  diffError: string;
  loading: boolean;
  connected: boolean;
}

export const store: Store = $state({
  meta: null,
  prs: [],
  loaded: false,
  query: '',
  diff: null,
  diffFor: null,
  diffError: '',
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

export function currentPR(): PRStatus | null {
  const r = router.route;
  if (r.name !== 'review') return null;
  return store.prs.find((p) => p.number === r.pr) ?? null;
}

export function selectedResource(): DiffResource | null {
  const r = router.route;
  if (r.name !== 'review' || !r.resource) return null;
  return store.diff?.resources?.find((res) => res.id === r.resource) ?? null;
}

// ---- navigation -----------------------------------------------------------

export function openPR(n: number): void {
  navigate({ name: 'review', pr: n, tab: 'overview', resource: null });
}

export function openDiffs(n: number, resource: string | null = null): void {
  navigate({ name: 'review', pr: n, tab: 'diffs', resource });
}

export function setTab(tab: Tab): void {
  const r = router.route;
  if (r.name !== 'review') return;
  navigate({ name: 'review', pr: r.pr, tab, resource: r.resource });
}

export function goList(): void {
  navigate({ name: 'list' });
}

export function adjacentPR(delta: number): void {
  const r = router.route;
  if (r.name !== 'review') return;
  const i = store.prs.findIndex((p) => p.number === r.pr);
  const j = i + delta;
  if (i >= 0 && j >= 0 && j < store.prs.length) openPR(store.prs[j].number);
}

export function adjacentResource(delta: number): void {
  const r = router.route;
  if (r.name !== 'review') return;
  const list = store.diff?.resources ?? [];
  if (list.length === 0) return;
  const i = r.resource ? list.findIndex((res) => res.id === r.resource) : -1;
  const j = Math.min(Math.max(i + delta, 0), list.length - 1);
  openDiffs(r.pr, list[j].id);
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
  store.loading = env.status === 'pending' || env.status === 'running';
  if (env.status === 'ready' && env.diff) {
    store.diff = env.diff;
    injectChroma(env.diff.chromaCss);
    store.diffError = '';
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
  const pr = store.prs.find((p) => p.number === ev.number);
  if (pr) {
    pr.status = ev.status!;
    pr.error = ev.error;
  }
  // The list endpoint carries the signal summary; re-pull it so badges update.
  if (ev.status === 'ready' || ev.status === 'error') {
    void loadPRs();
    if (ev.number === store.diffFor) void loadDiff(ev.number);
  }
}
