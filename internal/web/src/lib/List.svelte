<script lang="ts">
  import type { PRStatus } from './types';
  import { store, filteredPRs, openPR } from './store.svelte';
  import { clock, timeAgo, absolute } from './time.svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import Avatar from './Avatar.svelte';
  import Copy from './Copy.svelte';
  import Footer from './Footer.svelte';
  import {
    mdiAlertOctagon,
    mdiAlert,
    mdiPackageVariantClosed,
    mdiAlertCircleOutline,
    mdiFileDocumentOutline,
    mdiClockOutline,
    mdiRefresh,
    mdiTagOutline,
    mdiSourceBranch,
    mdiSourcePull,
    mdiFilterOutline,
    mdiChevronRight,
    mdiChevronDown,
    mdiCheckCircleOutline,
    mdiTrayFull,
    mdiLoading,
    mdiConsoleLine,
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

  // A '#'-prefixed color for a label dot, or '' when the forge gave no usable
  // hex (e.g. GitLab) — validated so a stray value can't reach the style binding.
  const labelColor = (l: { color?: string }) =>
    l.color && /^[0-9a-fA-F]{3,8}$/.test(l.color) ? `#${l.color}` : '';
</script>

{#snippet allClear()}
  <!-- An empty review queue is the success state — inbox zero, not an error. -->
  <div class="all-clear">
    <Icon path={mdiCheckCircleOutline} size={36} />
    <p>All caught up — no open pull requests.</p>
  </div>
{/snippet}

{#snippet prCard(pr: PRStatus)}
  <li class="card-li">
    <!-- Open PRs with danger warnings get a red edge (see .card.danger). -->
    <button
      class="card"
      class:merged={!pr.open}
      class:danger={pr.open && (pr.signals?.danger ?? 0) > 0}
      onclick={() => openPR(pr.number)}
    >
      <div class="card-top">
        <span class="dot {pr.open ? `dot-${pr.status}` : 'dot-merged'}"></span>
        <span class="card-title">{pr.title}</span>
      </div>
      <div class="card-meta">
        <span class="pr-id"><Icon path={mdiSourcePull} size={12} /> #{pr.number}</span>
        <span class="card-author"><Avatar src={pr.authorAvatar} size={15} /> {pr.author || 'unknown'}</span>
        {#if pr.draft}<span class="tag">draft</span>{/if}
        {#if pr.baseRef && defaultBase && pr.baseRef !== defaultBase}
          <span class="tag base-tag" title={`Targets ${pr.baseRef}, not the default branch`}>
            <Icon path={mdiSourceBranch} size={11} /> {pr.baseRef}
          </span>
        {/if}
        {#if !pr.open && pr.closedAt}
          <span class="ago" title={`Merged ${absolute(pr.closedAt)}`}><Icon path={mdiClockOutline} size={12} /> merged {timeAgo(pr.closedAt, clock.now)}</span>
        {:else}
          {#if pr.createdAt}
            <span class="ago" title={`Opened ${absolute(pr.createdAt)}`}><Icon path={mdiClockOutline} size={12} /> opened {timeAgo(pr.createdAt, clock.now)}</span>
          {/if}
          {#if pr.updatedAt}
            <span class="ago" title={`Last rendered ${absolute(pr.updatedAt)}`}><Icon path={mdiRefresh} size={12} /> {timeAgo(pr.updatedAt, clock.now)}</span>
          {/if}
        {/if}
        {#if pr.labels?.length}
          <span class="labels"><Icon path={mdiTagOutline} size={12} />{#each pr.labels.slice(0, 4) as l}<span class="label">{#if labelColor(l)}<span class="label-dot" style:background-color={labelColor(l)}></span>{/if}{l.name}</span>{/each}</span>
        {/if}
        <span class="spacer"></span>
        {#if pr.signals}
          <span class="badges">
            {#if pr.refreshError}
              <span class="badge caution" title="Couldn't refresh — showing the last render"><Icon path={mdiRefresh} size={13} /></span>
            {/if}
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
    {#if pr.mergeCommand}
      <div class="card-actions">
        <Copy text={pr.mergeCommand} label="Copy merge command" icon={mdiConsoleLine} />
      </div>
    {/if}
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
    {#if store.prs.length === 0}
      {@render allClear()}
    {:else}
      <p class="empty">No pull requests match your filter.</p>
    {/if}
  {:else}
    {#if openPrs.length}
      <ul class="cards">
        {#each openPrs as pr (pr.number)}{@render prCard(pr)}{/each}
      </ul>
    {:else}
      {@render allClear()}
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

  <Footer />
</div>
