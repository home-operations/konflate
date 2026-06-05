<script lang="ts">
  import type { DiffResource, SideCell } from './types';
  import { currentPR } from './store.svelte';
  import { isViewed, toggleViewed } from './viewed.svelte';
  import Icon from './Icon.svelte';
  import Copy from './Copy.svelte';
  import { mdiCheckCircle, mdiCircleOutline, mdiUnfoldMoreHorizontal, mdiUnfoldLessHorizontal } from './icons';

  let { resource }: { resource: DiffResource } = $props();
  const pr = $derived(currentPR());

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

  // Default to split on wide screens, unified on narrow; remember the override.
  function defaultMode(): 'unified' | 'split' {
    const saved = localStorage.getItem('konflate-diffmode');
    if (saved === 'unified' || saved === 'split') return saved;
    return window.innerWidth >= 1400 ? 'split' : 'unified';
  }
  let mode = $state<'unified' | 'split'>(defaultMode());
  function setMode(m: 'unified' | 'split') {
    mode = m;
    localStorage.setItem('konflate-diffmode', m);
  }

  const viewed = $derived(pr ? isViewed(pr.number, pr.headSha, resource.id) : false);
  const cellClass = (c: SideCell) => (c.kind === 'blank' ? 'side-blank' : `row-${c.kind}`);
</script>

<div class="res-header">
  <span class="res-status status-{resource.status}">{resource.status}</span>
  <span class="res-title">{resource.title}</span>
  <Copy text={resource.title} label="Copy resource identifier" />
  <span class="res-counts"><span class="add">+{resource.add}</span><span class="del">-{resource.del}</span></span>
  <div class="view-toggle">
    <button class:active={mode === 'unified'} onclick={() => setMode('unified')}>Unified</button>
    <button class:active={mode === 'split'} onclick={() => setMode('split')}>Split</button>
  </div>
  {#if pr}
    <button
      class="btn viewed-btn"
      class:on={viewed}
      onclick={() => toggleViewed(pr.number, pr.headSha, resource.id)}
      title="Mark viewed (v)"
    >
      <Icon path={viewed ? mdiCheckCircle : mdiCircleOutline} size={15} /> Viewed
    </button>
  {/if}
</div>

{#key resource.id}
  {#if mode === 'unified'}
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
              <!-- chroma-produced, HTML-escaped token spans; CSP blocks inline scripts -->
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
