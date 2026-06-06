<script lang="ts" module>
  // View mode is shared module state: every resource renders stacked in one
  // pane, so the toggle in any sticky header switches them all at once.
  // Default to split on wide screens, unified on narrow; remember the override.
  // Module state initializers are guarded so importing this file never throws
  // outside a browser (tests, a future SSR build).
  function defaultMode(): 'unified' | 'split' {
    if (typeof window === 'undefined') return 'unified';
    const saved = localStorage.getItem('konflate-diffmode');
    if (saved === 'unified' || saved === 'split') return saved;
    return window.innerWidth >= 1400 ? 'split' : 'unified';
  }
  const view = $state<{ mode: 'unified' | 'split' }>({ mode: defaultMode() });
  function setMode(m: 'unified' | 'split') {
    view.mode = m;
    localStorage.setItem('konflate-diffmode', m);
  }

  // Side-by-side split is unreadable at phone width — force unified there (and
  // hide the toggle). Tracks the viewport live so a rotate/resize updates it.
  const vp = $state({ narrow: false });
  if (typeof window !== 'undefined') {
    const mq = window.matchMedia('(max-width: 640px)');
    vp.narrow = mq.matches;
    mq.addEventListener('change', () => (vp.narrow = mq.matches));
  }
</script>

<script lang="ts">
  // SECURITY — the {@html} trust boundary. Every {@html} below renders
  // `row.html` / cell `.html`, which MUST only ever be the server's
  // Chroma-rendered diff lines: HTML-escaped token text wrapped in
  // <span class="..."> tags. There is no client-side sanitization — the
  // guarantees are (1) the server escapes all file content before wrapping it,
  // and (2) the CSP blocks inline scripts as a backstop. Never route any other
  // value (PR titles, file paths, warnings — anything forge-controlled) into
  // these fields, and never relax this without adding client-side sanitization.
  import type { DiffResource, SideCell } from './types';
  import { store } from './store.svelte';
  import Icon from './Icon.svelte';
  import Copy from './Copy.svelte';
  import { mdiAlertOctagon, mdiAlert, mdiUnfoldMoreHorizontal, mdiUnfoldLessHorizontal } from './icons';

  let { resource }: { resource: DiffResource } = $props();

  // This resource's lint warnings, shown in its sticky header — in the stacked
  // scroll the global danger strip scrolls away, so the warning rides along
  // with the diff it belongs to. Matched the way Overview deep-links them.
  const warns = $derived((store.diff?.warnings ?? []).filter((w) => w.resource === resource.title));
  const dangers = $derived(warns.filter((w) => w.level === 'danger'));
  const cautions = $derived(warns.filter((w) => w.level !== 'danger'));
  const detail = (list: { detail: string }[]) => list.map((w) => w.detail).join('\n');

  // Folded-context expanders. Keyed by resource id + fold id so the same gap id
  // ("g0") across different resources never collide, and each resource keeps its
  // own expand state when you navigate away and back.
  let expanded = $state<Record<string, boolean>>({});
  const isExpanded = (fold?: string): boolean => !!(fold && expanded[`${resource.id}:${fold}`]);
  function toggleFold(fold?: string): void {
    if (fold) expanded[`${resource.id}:${fold}`] = !expanded[`${resource.id}:${fold}`];
  }
  const expandLabel = (count?: number): string =>
    `Expand ${count} unchanged ${count === 1 ? 'line' : 'lines'}`;

  const renderMode = $derived(vp.narrow ? 'unified' : view.mode);

  const cellClass = (c: SideCell) => (c.kind === 'blank' ? 'side-blank' : `row-${c.kind}`);
</script>

<div class="res-header">
  <span class="res-status status-{resource.status}">{resource.status}</span>
  <span class="res-title">{resource.title}</span>
  <Copy text={resource.title} label="Copy resource identifier" />
  {#if dangers.length}
    <span class="badge danger" title={detail(dangers)}>
      <Icon path={mdiAlertOctagon} size={13} /> {dangers.length > 1 ? dangers.length : ''}
    </span>
  {/if}
  {#if cautions.length}
    <span class="badge caution" title={detail(cautions)}>
      <Icon path={mdiAlert} size={13} /> {cautions.length > 1 ? cautions.length : ''}
    </span>
  {/if}
  <!-- Zero counts are hidden, matching the tree rail. -->
  <span class="res-counts"
    >{#if resource.add}<span class="add">+{resource.add}</span>{/if}{#if resource.del}<span class="del">-{resource.del}</span>{/if}</span
  >
  {#if !vp.narrow}
    <div class="view-toggle">
      <button class:active={view.mode === 'unified'} aria-pressed={view.mode === 'unified'} onclick={() => setMode('unified')}>Unified</button>
      <button class:active={view.mode === 'split'} aria-pressed={view.mode === 'split'} onclick={() => setMode('split')}>Split</button>
    </div>
  {/if}
</div>

{#key resource.id}
  {#if renderMode === 'unified'}
    <table class="diff chroma unified">
      <tbody>
        {#each resource.unified as row}
          {#if row.hunk}
            <tr class="row-expand">
              <td colspan="4">
                <button class="expand-btn" onclick={() => toggleFold(row.fold)}>
                  <Icon path={isExpanded(row.fold) ? mdiUnfoldLessHorizontal : mdiUnfoldMoreHorizontal} size={14} />
                  {isExpanded(row.fold) ? 'Collapse' : expandLabel(row.count)}
                </button>
              </td>
            </tr>
          {:else if row.folded}
            {#if isExpanded(row.fold)}
              <tr class="row-ctx folded">
                <td class="gutter num">{row.oldNo || ''}</td>
                <td class="gutter num">{row.newNo || ''}</td>
                <td class="gutter sign"></td>
                <td class="code">{@html row.html ?? ''}</td>
              </tr>
            {/if}
          {:else}
            <tr class="row-{row.kind ?? 'ctx'}">
              <td class="gutter num">{row.oldNo || ''}</td>
              <td class="gutter num">{row.newNo || ''}</td>
              <td class="gutter sign">{row.kind === 'add' ? '+' : row.kind === 'del' ? '-' : ''}</td>
              <!-- chroma-produced, HTML-escaped token spans; CSP blocks inline
                   scripts — see the trust-boundary note at the top of this file -->
              <td class="code">{@html row.html ?? ''}</td>
            </tr>
          {/if}
        {/each}
      </tbody>
    </table>
  {:else}
    <table class="diff chroma split">
      <colgroup>
        <col class="col-num" /><col class="col-code" />
        <col class="col-num" /><col class="col-code" />
      </colgroup>
      <tbody>
        {#each resource.side as row}
          {#if row.hunk}
            <tr class="row-expand">
              <td colspan="4">
                <button class="expand-btn" onclick={() => toggleFold(row.fold)}>
                  <Icon path={isExpanded(row.fold) ? mdiUnfoldLessHorizontal : mdiUnfoldMoreHorizontal} size={14} />
                  {isExpanded(row.fold) ? 'Collapse' : expandLabel(row.count)}
                </button>
              </td>
            </tr>
          {:else if row.folded}
            {#if isExpanded(row.fold)}
              <tr class="folded">
                <td class="gutter num">{row.left.no || ''}</td>
                <td class="code {cellClass(row.left)}"
                  >{#if row.left.kind !== 'blank'}{@html row.left.html ?? ''}{/if}</td
                >
                <td class="gutter num">{row.right.no || ''}</td>
                <td class="code {cellClass(row.right)}"
                  >{#if row.right.kind !== 'blank'}{@html row.right.html ?? ''}{/if}</td
                >
              </tr>
            {/if}
          {:else}
            <tr>
              <td class="gutter num">{row.left.no || ''}</td>
              <td class="code {cellClass(row.left)}"
                >{#if row.left.kind !== 'blank'}{@html row.left.html ?? ''}{/if}</td
              >
              <td class="gutter num">{row.right.no || ''}</td>
              <td class="code {cellClass(row.right)}"
                >{#if row.right.kind !== 'blank'}{@html row.right.html ?? ''}{/if}</td
              >
            </tr>
          {/if}
        {/each}
      </tbody>
    </table>
  {/if}
{/key}
