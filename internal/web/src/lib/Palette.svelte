<script lang="ts">
  // The Cmd/Ctrl+K command palette: jump to any PR from anywhere. The input
  // shares the list filter's query grammar (free text + status:/author:/base:/
  // label: tokens); rows preview each PR's review signals so "why it matters"
  // is visible before committing; recent searches persist in localStorage.
  import type { PRStatus } from './types';
  import { palette, togglePalette } from './keyboard.svelte';
  import { store, parseQuery, matchesQuery, openPR } from './store.svelte';
  import Icon from './Icon.svelte';
  import Avatar from './Avatar.svelte';
  import {
    mdiMagnify,
    mdiHistory,
    mdiSourcePull,
    mdiAlert,
    mdiAlertCircleOutline,
    mdiFilterOutline,
  } from './icons';

  const RECENTS_KEY = 'konflate-recents';
  function loadRecents(): string[] {
    try {
      const v = JSON.parse(localStorage.getItem(RECENTS_KEY) ?? '[]') as unknown;
      return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
    } catch {
      return [];
    }
  }
  function recordRecent(query: string): void {
    const q = query.trim();
    if (!q) return;
    const rs = [q, ...loadRecents().filter((x) => x !== q)].slice(0, 6);
    try {
      localStorage.setItem(RECENTS_KEY, JSON.stringify(rs));
    } catch {
      /* storage unavailable — recents just don't persist */
    }
  }

  let q = $state('');
  let idx = $state(0);
  let recents = $state(loadRecents());

  // Fresh state on every open: the component stays mounted between opens, so
  // re-read the persisted recents and drop the previous query.
  $effect(() => {
    if (palette.open) {
      recents = loadRecents();
      q = '';
      idx = 0;
    }
  });

  const parsed = $derived(parseQuery(q));

  // Matching PRs, risk first: render failures (unknown risk) then cautions as a
  // bonus on top of recency, so the PR that most needs eyes is one Enter away.
  const matches = $derived.by(() => {
    const score = (p: PRStatus): number => {
      const s = p.signals;
      const t = Date.parse(p.updatedAt ?? '') || 0;
      // A failed render means unknown risk — weight it like render failures.
      const failed = s?.failures || p.status === 'error';
      return t / 1e9 + (failed ? 50 : 0) + (s?.caution ? 20 : 0);
    };
    return store.prs
      .filter((p) => matchesQuery(p, parsed))
      .sort((a, b) => score(b) - score(a))
      .slice(0, 8);
  });

  // Facet examples offered while the query is empty.
  const suggestions = [
    { query: 'status:caution', hint: 'only PRs with cautions' },
    { query: 'author:renovate', hint: 'only renovate PRs' },
    { query: 'status:merged', hint: 'recently merged PRs' },
  ];

  // The flat row list the arrows walk: recents (empty query only), then PRs,
  // then the facet examples (empty query only).
  type Row =
    | { kind: 'recent'; query: string }
    | { kind: 'pr'; pr: PRStatus }
    | { kind: 'suggestion'; query: string; hint: string };
  const rows = $derived.by(() => {
    const out: Row[] = [];
    if (!q.trim()) for (const r of recents) out.push({ kind: 'recent', query: r });
    for (const pr of matches) out.push({ kind: 'pr', pr });
    if (!q.trim()) for (const s of suggestions) out.push({ kind: 'suggestion', ...s });
    return out;
  });

  // Clamp the cursor when the rows change under it (typing narrows the list).
  $effect(() => {
    if (idx >= rows.length) idx = Math.max(0, rows.length - 1);
  });

  // First row of each kind, computed once per rows change — the group labels
  // render above these.
  const firstPrIdx = $derived(rows.findIndex((r) => r.kind === 'pr'));
  const firstSuggestionIdx = $derived(rows.findIndex((r) => r.kind === 'suggestion'));

  function commit(row: Row | undefined): void {
    if (!row) return;
    if (row.kind === 'pr') {
      recordRecent(q);
      togglePalette();
      openPR(row.pr.number);
    } else {
      q = row.query;
      idx = 0;
    }
  }

  function onKeydown(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      togglePalette();
    } else if (e.key === 'ArrowDown' || (e.key === 'Tab' && !e.shiftKey)) {
      idx = rows.length ? (idx + 1) % rows.length : 0;
    } else if (e.key === 'ArrowUp' || (e.key === 'Tab' && e.shiftKey)) {
      idx = rows.length ? (idx - 1 + rows.length) % rows.length : 0;
    } else if (e.key === 'Enter') {
      commit(rows[idx]);
    } else {
      return;
    }
    e.preventDefault();
  }

  function focusOnMount(node: HTMLElement): void {
    node.focus();
  }

  // Split a title around the free-text match so the matched run renders in a
  // <mark> without ever passing forge-controlled text through {@html}.
  function highlight(title: string): [string, string, string] {
    const needle = parsed.text;
    if (!needle) return [title, '', ''];
    const i = title.toLowerCase().indexOf(needle);
    if (i < 0) return [title, '', ''];
    return [title.slice(0, i), title.slice(i, i + needle.length), title.slice(i + needle.length)];
  }

  const dotClass = (p: PRStatus): string => (p.open ? `dot-${p.status}` : 'dot-merged');
</script>

{#if palette.open}
  <div class="palette-overlay">
    <button class="help-backdrop" aria-label="Close search" onclick={togglePalette}></button>
    <!-- The keydown handler lives on the dialog (not the input) so Tab cycles
         the rows — and never escapes to the page behind — wherever focus sits
         inside the palette. aria-modal marks the background inert for AT. -->
    <div class="palette" role="dialog" aria-modal="true" aria-label="Search pull requests" tabindex="-1" onkeydown={onKeydown}>
      <div class="palette-input">
        <Icon path={mdiMagnify} size={16} />
        <!-- svelte-ignore a11y_autofocus -->
        <input
          bind:value={q}
          use:focusOnMount
          oninput={() => (idx = 0)}
          placeholder="Search pull requests… (status: author: base: label:)"
          aria-label="Search pull requests"
        />
        <span class="palette-esc"><kbd>Esc</kbd></span>
      </div>

      <div class="palette-body">
        {#if !q.trim() && recents.length}
          <div class="group-label">Recent</div>
        {/if}
        {#each rows as row, i (row.kind + (row.kind === 'pr' ? row.pr.number : row.query))}
          {#if i === firstPrIdx}
            <div class="group-label">Pull requests</div>
          {/if}
          {#if i === firstSuggestionIdx}
            <div class="group-label">Try</div>
          {/if}
          <button
            class="palette-row"
            class:active={i === idx}
            onclick={() => commit(row)}
            onmouseenter={() => (idx = i)}
          >
            {#if row.kind === 'pr'}
              {@const [pre, hit, post] = highlight(row.pr.title)}
              <span class="dot {dotClass(row.pr)}"></span>
              <span class="row-main">
                <span class="row-title">{pre}{#if hit}<mark>{hit}</mark>{/if}{post}</span>
                <span class="row-sub">
                  <Icon path={mdiSourcePull} size={11} /> #{row.pr.number} ·
                  <Avatar src={row.pr.authorAvatar} size={12} />
                  {row.pr.author} · {row.pr.baseRef}
                </span>
              </span>
              <span class="row-right">
                {#if row.pr.signals?.caution}<span class="badge caution"><Icon path={mdiAlert} size={12} /> {row.pr.signals.caution}</span>{/if}
                {#if row.pr.signals?.failures}<span class="badge danger"><Icon path={mdiAlertCircleOutline} size={12} /> {row.pr.signals.failures}</span>{/if}
              </span>
            {:else}
              <span class="row-glyph"><Icon path={row.kind === 'recent' ? mdiHistory : mdiFilterOutline} size={14} /></span>
              <span class="row-main">
                <span class="row-title mono">{row.query}</span>
                {#if row.kind === 'suggestion'}<span class="row-sub">{row.hint}</span>{/if}
              </span>
            {/if}
          </button>
        {/each}
        {#if q.trim() && !matches.length}
          <p class="palette-empty">No pull requests match “{q}”.</p>
        {/if}
      </div>

      <div class="palette-footer">
        <span><kbd>↑</kbd><kbd>↓</kbd> navigate</span>
        <span><kbd>⏎</kbd> open</span>
        <span class="foot-gap"></span>
        <span>{matches.length} match{matches.length === 1 ? '' : 'es'}</span>
      </div>
    </div>
  </div>
{/if}
