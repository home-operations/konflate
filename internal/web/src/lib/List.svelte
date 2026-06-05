<script lang="ts">
  import type { PRStatus } from './types';
  import { store, filteredPRs, openPR } from './store.svelte';
  import { clock, timeAgo, absolute } from './time.svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import {
    mdiAlertOctagon,
    mdiAlert,
    mdiPackageVariantClosed,
    mdiAlertCircleOutline,
    mdiFileDocumentOutline,
    mdiChevronRight,
    mdiChevronDown,
    mdiSourceMerge,
    mdiTrayFull,
    mdiLoading,
  } from './icons';

  const prs = $derived(filteredPRs());
  const openPrs = $derived(prs.filter((p) => p.open));
  const mergedPrs = $derived(prs.filter((p) => !p.open));

  // The "recently merged" shelf is collapsed by default; expand on click, or
  // automatically while a filter is active so a search can reach merged PRs.
  let mergedExpanded = $state(false);
  const showMerged = $derived(mergedExpanded || store.query.trim() !== '');

  // pending/running render as icons below and ready carries signal badges, so
  // this fallback only labels the terminal error state.
  const statusLabel: Record<string, string> = { error: 'failed' };
</script>

{#snippet prCard(pr: PRStatus)}
  <li>
    <button class="card" class:merged={!pr.open} onclick={() => openPR(pr.number)}>
      <div class="card-top">
        <span class="dot {pr.open ? `dot-${pr.status}` : 'dot-merged'}"></span>
        <span class="pr-num">#{pr.number}</span>
        <span class="card-title">{pr.title}</span>
        {#if pr.draft}<span class="tag">draft</span>{/if}
        {#if !pr.open}<span class="tag merged-tag"><Icon path={mdiSourceMerge} size={11} /> merged</span>{/if}
      </div>
      <div class="card-meta">
        <span class="card-author">{pr.author || 'unknown'}</span>
        {#if !pr.open && pr.closedAt}
          <span class="ago" title={`Merged ${absolute(pr.closedAt)}`}>merged {timeAgo(pr.closedAt, clock.now)}</span>
        {:else if pr.updatedAt}
          <span class="ago" title={`Last refreshed ${absolute(pr.updatedAt)}`}>{timeAgo(pr.updatedAt, clock.now)}</span>
        {/if}
        {#if pr.labels?.length}
          <span class="labels">{#each pr.labels.slice(0, 4) as l}<span class="label">{l}</span>{/each}</span>
        {/if}
        <span class="spacer"></span>
        {#if pr.signals}
          <span class="badges">
            {#if pr.signals.danger}
              <span class="badge danger" title="danger warnings"><Icon path={mdiAlertOctagon} size={13} /> {pr.signals.danger}</span>
            {/if}
            {#if pr.signals.caution}
              <span class="badge caution" title="cautions"><Icon path={mdiAlert} size={13} /> {pr.signals.caution}</span>
            {/if}
            {#if pr.signals.images}
              <span class="badge" title="image changes"><Icon path={mdiPackageVariantClosed} size={13} /> {pr.signals.images}</span>
            {/if}
            {#if pr.signals.failures}
              <span class="badge danger" title="render failures"><Icon path={mdiAlertCircleOutline} size={13} /> {pr.signals.failures}</span>
            {/if}
            <span class="badge muted" title="changed resources">
              <Icon path={mdiFileDocumentOutline} size={13} /> {pr.signals.resources}
            </span>
          </span>
        {:else if pr.status === 'running'}
          <span class="card-status running"><Spinner size={14} /> rendering</span>
        {:else if pr.status === 'pending'}
          <span class="card-status"><Icon path={mdiTrayFull} size={13} /> queued</span>
        {:else}
          <span class="card-status s-{pr.status}">{statusLabel[pr.status] ?? pr.status}</span>
        {/if}
      </div>
    </button>
  </li>
{/snippet}

<div class="list-screen">
  <input
    class="pr-search"
    placeholder="Filter pull requests…"
    bind:value={store.query}
    aria-label="Filter pull requests"
  />

  {#if !store.loaded}
    <p class="empty"><Icon path={mdiLoading} spin /> Loading pull requests…</p>
  {:else if prs.length === 0}
    <p class="empty">
      {store.prs.length === 0 ? 'No open pull requests.' : 'No pull requests match your filter.'}
    </p>
  {:else}
    {#if openPrs.length}
      <ul class="cards">
        {#each openPrs as pr (pr.number)}{@render prCard(pr)}{/each}
      </ul>
    {:else}
      <p class="empty">No open pull requests.</p>
    {/if}

    {#if mergedPrs.length}
      <button class="group-head" onclick={() => (mergedExpanded = !mergedExpanded)} aria-expanded={showMerged}>
        <Icon path={showMerged ? mdiChevronDown : mdiChevronRight} size={16} />
        Recently merged <span class="group-count">{mergedPrs.length}</span>
      </button>
      {#if showMerged}
        <ul class="cards merged-cards">
          {#each mergedPrs as pr (pr.number)}{@render prCard(pr)}{/each}
        </ul>
      {/if}
    {/if}
  {/if}
</div>
