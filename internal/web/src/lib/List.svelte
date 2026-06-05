<script lang="ts">
  import type { PRStatus } from './types';
  import { store, filteredPRs, openPR } from './store.svelte';
  import { clock, timeAgo, absolute } from './time.svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import Avatar from './Avatar.svelte';
  import {
    mdiAlertOctagon,
    mdiAlert,
    mdiPackageVariantClosed,
    mdiAlertCircleOutline,
    mdiFileDocumentOutline,
    mdiClockOutline,
    mdiTagOutline,
    mdiSourceBranch,
    mdiFilterOutline,
    mdiChevronRight,
    mdiChevronDown,
    mdiSourceMerge,
    mdiTrayFull,
    mdiLoading,
  } from './icons';

  const prs = $derived(filteredPRs());
  const openPrs = $derived(prs.filter((p) => p.open));
  const mergedPrs = $derived(prs.filter((p) => !p.open));

  // At-a-glance health of the open set, shown above the list.
  const summary = $derived.by(() => ({
    open: openPrs.length,
    danger: openPrs.filter((p) => (p.signals?.danger ?? 0) > 0).length,
    failed: openPrs.filter((p) => p.status === 'error').length,
    rendering: openPrs.filter((p) => p.status === 'pending' || p.status === 'running').length,
    merged: mergedPrs.length,
  }));

  // The default base branch is whatever most open PRs target. A PR whose base
  // differs is flagged on its card so a non-default target (konflate reviews all
  // open PRs regardless of base) isn't mistaken for one against the default.
  const defaultBase = $derived.by(() => {
    const counts = new Map<string, number>();
    for (const p of openPrs) if (p.baseRef) counts.set(p.baseRef, (counts.get(p.baseRef) ?? 0) + 1);
    let best = '';
    let max = 0;
    for (const [ref, c] of counts) if (c > max) [best, max] = [ref, c];
    return best;
  });

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
        {#if pr.baseRef && defaultBase && pr.baseRef !== defaultBase}
          <span class="tag base-tag" title={`Targets ${pr.baseRef}, not the default branch`}>
            <Icon path={mdiSourceBranch} size={11} /> {pr.baseRef}
          </span>
        {/if}
        {#if !pr.open}<span class="tag merged-tag"><Icon path={mdiSourceMerge} size={11} /> merged</span>{/if}
      </div>
      <div class="card-meta">
        <span class="card-author"><Avatar src={pr.authorAvatar} size={15} /> {pr.author || 'unknown'}</span>
        {#if !pr.open && pr.closedAt}
          <span class="ago" title={`Merged ${absolute(pr.closedAt)}`}><Icon path={mdiClockOutline} size={12} /> merged {timeAgo(pr.closedAt, clock.now)}</span>
        {:else if pr.updatedAt}
          <span class="ago" title={`Last refreshed ${absolute(pr.updatedAt)}`}><Icon path={mdiClockOutline} size={12} /> {timeAgo(pr.updatedAt, clock.now)}</span>
        {/if}
        {#if pr.labels?.length}
          <span class="labels"><Icon path={mdiTagOutline} size={12} />{#each pr.labels.slice(0, 4) as l}<span class="label">{l}</span>{/each}</span>
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
        {:else if pr.open && pr.status === 'running'}
          <span class="card-status running"><Spinner size={14} /> rendering</span>
        {:else if pr.open && pr.status === 'pending'}
          <span class="card-status"><Icon path={mdiTrayFull} size={13} /> queued</span>
        {:else if pr.open}
          <span class="card-status s-{pr.status}">{statusLabel[pr.status] ?? pr.status}</span>
        {/if}
      </div>
    </button>
  </li>
{/snippet}

<div class="list-screen">
  <div class="pr-search-wrap">
    <Icon path={mdiFilterOutline} size={16} />
    <input
      class="pr-search"
      placeholder="Filter pull requests…"
      bind:value={store.query}
      aria-label="Filter pull requests"
    />
  </div>

  {#if store.loaded && openPrs.length}
    <div class="list-summary">
      <span class="sum-pill"><strong>{summary.open}</strong> open</span>
      {#if summary.danger}<span class="sum-pill danger"><strong>{summary.danger}</strong> danger</span>{/if}
      {#if summary.failed}<span class="sum-pill danger"><strong>{summary.failed}</strong> failed</span>{/if}
      {#if summary.rendering}<span class="sum-pill"><strong>{summary.rendering}</strong> rendering</span>{/if}
      {#if summary.merged}<span class="sum-pill merged"><strong>{summary.merged}</strong> merged</span>{/if}
    </div>
  {/if}

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
