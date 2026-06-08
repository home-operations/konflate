<script lang="ts">
  import type { PRStatus } from './types';
  import { store, filteredPRs, matchesStatus, sortPRs, openPR, ensurePreview, type StatusFilter } from './store.svelte';
  import { clock, timeAgo, absolute } from './time.svelte';
  import { slide } from 'svelte/transition';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import Avatar from './Avatar.svelte';
  import Footer from './Footer.svelte';
  import Breakable from './Breakable.svelte';
  import MergeCommand from './MergeCommand.svelte';
  import {
    mdiAlert,
    mdiPackageVariantClosed,
    mdiAlertCircleOutline,
    mdiFileDocumentOutline,
    mdiClockOutline,
    mdiRefresh,
    mdiTagOutline,
    mdiSourceBranch,
    mdiSourcePull,
    mdiClose,
    mdiFilterOutline,
    mdiSortVariant,
    mdiSortAscending,
    mdiSortDescending,
    mdiChevronRight,
    mdiChevronDown,
    mdiChevronUp,
    mdiCheckCircleOutline,
    mdiTrayFull,
    mdiUnfoldMoreHorizontal,
    mdiUnfoldLessHorizontal,
    mdiArrowUp,
  } from './icons';

  // Two filter stages: the text query narrows `prs` (which the summary pills
  // count, so the counts hold steady while a pill is active), then the active
  // pill narrows and the sort orders what's shown.
  const prs = $derived(filteredPRs());
  const shown = $derived(sortPRs(prs.filter((p) => matchesStatus(p, store.statusFilter))));
  const openPrs = $derived(shown.filter((p) => p.open));
  const mergedPrs = $derived(shown.filter((p) => !p.open));
  const openAll = $derived(prs.filter((p) => p.open));
  const filterActive = $derived(store.query.trim() !== '' || store.statusFilter !== '');

  // At-a-glance health of the open set, shown above the list — each pill is
  // also a toggle that filters the list down to that status.
  const summary = $derived.by(() => ({
    open: openAll.length,
    caution: openAll.filter((p) => (p.signals?.caution ?? 0) > 0).length,
    merged: prs.length - openAll.length,
  }));

  function toggleFilter(f: StatusFilter): void {
    store.statusFilter = store.statusFilter === f ? '' : f;
  }
  // A pill renders while it counts something — or while it's the active
  // filter, so it can't strand itself unclickable when its count hits zero.
  const showPill = (count: number, f: StatusFilter): boolean => count > 0 || store.statusFilter === f;

  // Picking a sort field resets to its natural direction (time newest-first,
  // name A→Z); the direction button then flips it.
  function onSortKeyChange(): void {
    store.sortDir = store.sort === 'name' ? 'asc' : 'desc';
  }
  function toggleSortDir(): void {
    store.sortDir = store.sortDir === 'asc' ? 'desc' : 'asc';
  }
  const sortDirLabel = $derived.by(() => {
    if (store.sort === 'name') return store.sortDir === 'asc' ? 'A → Z' : 'Z → A';
    return store.sortDir === 'desc' ? 'newest first' : 'oldest first';
  });

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
  const showMerged = $derived(mergedExpanded || filterActive);

  // Per-row inline summary: the chevron toggles it, lazy-loading the PR's diff
  // summary on first open (the row click still opens the full review). Keyed by
  // PR number; the data is cached in the store and keyed by headSha there.
  let expanded = $state<Record<number, boolean>>({});
  const isExpanded = (n: number): boolean => !!expanded[n];
  function toggleExpand(n: number, headSha: string): void {
    expanded[n] = !expanded[n];
    if (expanded[n]) ensurePreview(n, headSha);
  }

  // The visible rows that have a summary to toggle — open cards plus the merged
  // shelf when it's showing. Drives the expand/collapse-all control.
  const expandableRows = $derived([...openPrs, ...(showMerged ? mergedPrs : [])].filter((p) => p.signals));
  const allExpanded = $derived(expandableRows.length > 0 && expandableRows.every((p) => expanded[p.number]));
  function toggleExpandAll(): void {
    const next = !allExpanded;
    for (const p of expandableRows) {
      expanded[p.number] = next;
      if (next) ensurePreview(p.number, p.headSha);
    }
  }

  // Scroll-to-top: the list owns its scroll (.list-screen), so watch it and
  // surface a float button once the user is well past the top.
  let screenEl = $state<HTMLElement>();
  let scrolled = $state(false);
  function onScroll(): void {
    if (screenEl) scrolled = screenEl.scrollTop > 400;
  }
  function scrollToTop(): void {
    screenEl?.scrollTo({ top: 0, behavior: 'smooth' });
  }

  // Shorten an "algo:hexdigest" image ref so a digest-pinned bump doesn't blow
  // out the preview row; tags are short already and shown whole.
  function shortVer(v: string): string {
    if (!v) return '∅';
    const i = v.indexOf(':');
    if (i < 0) return v;
    const hex = v.slice(i + 1);
    return /^[0-9a-f]+$/i.test(hex) && hex.length > 12 ? `${v.slice(0, i + 1)}${hex.slice(0, 12)}…` : v;
  }

  // pending/running render as icons below and ready carries signal badges, so
  // this fallback only labels the terminal error state.
  const statusLabel: Record<string, string> = { error: 'failed', blocked: 'fork · not rendered' };

  // A '#'-prefixed color for a label dot, or '' when the forge gave no usable
  // hex (e.g. GitLab) — validated so a stray value can't reach the style binding.
  const labelColor = (l: { color?: string }) =>
    l.color && /^[0-9a-fA-F]{3,8}$/.test(l.color) ? `#${l.color}` : '';
</script>

{#snippet pill(f: StatusFilter, count: number, tone: string, hint: string)}
  <button
    class="sum-pill {tone}"
    class:active={store.statusFilter === f}
    aria-pressed={store.statusFilter === f}
    title={hint}
    onclick={() => toggleFilter(f)}
  >
    <strong>{count}</strong> {f}
  </button>
{/snippet}

{#snippet allClear()}
  <div class="all-clear">
    <Icon path={mdiCheckCircleOutline} size={36} />
    <p>All caught up — no open pull requests.</p>
  </div>
{/snippet}

<!-- The inline row summary, lazy-loaded into store.previews. Read-only — the row
     click still opens the full review. Ordered: copy, resource diffs, cautions,
     render failures, image changes. The copy command rides on the list data, so
     it shows immediately; the rest waits on the summary fetch. -->
{#snippet previewBody(pr: PRStatus)}
  {@const pv = store.previews[pr.number]}
  {#if pr.mergeCommand}
    <MergeCommand command={pr.mergeCommand} />
  {/if}

  {#if !pv || pv.state === 'loading'}
    <div class="pv-msg"><Spinner size={13} /> Loading summary…</div>
  {:else if pv.state === 'pending'}
    <div class="pv-msg"><Icon path={mdiTrayFull} size={14} /> Still rendering — open the PR to watch.</div>
  {:else if pv.state === 'error'}
    <div class="pv-msg pv-error"><Icon path={mdiAlertCircleOutline} size={14} /> {pv.error}</div>
  {:else}
    <div class="pv-group">
      <span class="pv-label">Resource diffs</span>
      <div class="pv-impact">
        {#if pv.summary && pv.summary.added}<span class="pv-stat add">+{pv.summary.added}</span>{/if}
        {#if pv.summary && pv.summary.changed}<span class="pv-stat chg">~{pv.summary.changed}</span>{/if}
        {#if pv.summary && pv.summary.removed}<span class="pv-stat del">−{pv.summary.removed}</span>{/if}
        {#if pv.impact}
          <span class="pv-dim"
            >{pv.impact.resources} {pv.impact.resources === 1 ? 'resource' : 'resources'} · {pv.impact.parents}
            {pv.impact.parents === 1 ? 'app' : 'apps'}{#if pv.impact.crds} · {pv.impact.crds} {pv.impact.crds === 1 ? 'CRD' : 'CRDs'}{/if}</span
          >
        {/if}
        {#if pv.truncated}<span class="pv-trunc">{pv.truncated} not shown</span>{/if}
      </div>
    </div>

    {#if pv.warnings?.length}
      <div class="pv-group">
        <span class="pv-label">Cautions</span>
        <ul class="pv-list">
          {#each pv.warnings.slice(0, 8) as w}
            <li class="pv-caution"><Icon path={mdiAlert} size={13} /> <span class="pv-res">{w.resource}</span> <span class="pv-detail">{w.detail}</span></li>
          {/each}
          {#if pv.warnings.length > 8}<li class="pv-more">+{pv.warnings.length - 8} more cautions</li>{/if}
        </ul>
      </div>
    {/if}

    {#if pv.failures?.length}
      <div class="pv-group">
        <span class="pv-label">Render failures</span>
        <ul class="pv-list">
          {#each pv.failures as f}
            <li class="pv-failure"><Icon path={mdiAlertCircleOutline} size={13} /> <span class="pv-res">{f.parent}</span> <span class="pv-detail">{f.message}</span></li>
          {/each}
        </ul>
      </div>
    {/if}

    {#if pv.images?.length}
      <div class="pv-group">
        <span class="pv-label">Image changes</span>
        <ul class="pv-list">
          {#each pv.images.slice(0, 6) as img}
            <li class="pv-image"><Icon path={mdiPackageVariantClosed} size={13} /> <span class="pv-res">{img.name}</span> <span class="pv-delta">{shortVer(img.from)} → {shortVer(img.to)}</span></li>
          {/each}
          {#if pv.images.length > 6}<li class="pv-more">+{pv.images.length - 6} more images</li>{/if}
        </ul>
      </div>
    {/if}

    {#if !pv.warnings?.length && !pv.images?.length && !pv.failures?.length}
      <div class="pv-msg pv-clean"><Icon path={mdiCheckCircleOutline} size={14} /> No cautions, image changes, or render failures.</div>
    {/if}
  {/if}
{/snippet}

{#snippet prCard(pr: PRStatus)}
  <li class="card-li" class:expanded={isExpanded(pr.number)}>
    <div
      class="card-shell"
      class:merged={!pr.open}
      class:caution={pr.open && (pr.signals?.caution ?? 0) > 0}
    >
      <button class="card" onclick={() => openPR(pr.number)}>
        <div class="card-top">
        <span class="dot {pr.open ? `dot-${pr.status}` : 'dot-merged'}"></span>
        <span class="card-title"><Breakable text={pr.title} /></span>
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
        {:else if pr.createdAt}
          <!-- Created date only — render freshness lives on the review screen, and
               a failed refresh is flagged by the badge below. The clock icon labels
               it (full "Opened …" date in the title), so the word would just crowd
               the row (worst on mobile). -->
          <span class="ago" title={`Opened ${absolute(pr.createdAt)}`}><Icon path={mdiClockOutline} size={12} /> {timeAgo(pr.createdAt, clock.now)}</span>
        {/if}
        {#if pr.labels?.length}
          <span class="labels"><Icon path={mdiTagOutline} size={12} />{#each pr.labels.slice(0, 4) as l}<span class="label">{#if labelColor(l)}<span class="label-dot" style:background-color={labelColor(l)}></span>{/if}{l.name}</span>{/each}</span>
        {/if}
        <span class="spacer"></span>
        {#if pr.signals}
          <span class="badges">
            {#if pr.refreshError}
              <span class="badge warn" title="Couldn't refresh — showing the last render"><Icon path={mdiRefresh} size={13} /></span>
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
      <!-- Disclosure for the inline summary. Sibling of the card <button> (a
           button can't nest), only shown once the PR has rendered signals. -->
      {#if pr.signals}
        <button
          class="card-expand"
          aria-expanded={isExpanded(pr.number)}
          aria-label={isExpanded(pr.number) ? `Hide summary for #${pr.number}` : `Show summary for #${pr.number}`}
          onclick={() => toggleExpand(pr.number, pr.headSha)}
        >
          <Icon path={isExpanded(pr.number) ? mdiChevronUp : mdiChevronDown} size={18} />
        </button>
      {/if}
    </div>
    {#if isExpanded(pr.number)}
      <div class="card-preview" transition:slide={{ duration: 120 }}>
        {@render previewBody(pr)}
      </div>
    {/if}
  </li>
{/snippet}

<div class="list-screen" bind:this={screenEl} onscroll={onScroll}>
  <!-- The toolbar lives in the body, sharing the cards' 960px column and left
       edge — it's the list's filter, not app chrome. -->
  <div class="list-toolbar">
    <label class="search-box">
      <Icon path={mdiFilterOutline} size={15} />
      <input
        class="pr-search"
        placeholder="Filter pull requests… (try status:caution or author:renovate)"
        bind:value={store.query}
        aria-label="Filter pull requests"
      />
      {#if store.query}
        <button class="clear-btn" onclick={() => (store.query = '')} aria-label="Clear filter">
          <Icon path={mdiClose} size={13} />
        </button>
      {/if}
      <span class="key-hint"><kbd>/</kbd></span>
    </label>
    <div class="sort">
      <label class="sort-field" title="Sort field">
        <Icon path={mdiSortVariant} size={15} />
        <select bind:value={store.sort} onchange={onSortKeyChange} aria-label="Sort field">
          <option value="created">created</option>
          <option value="refreshed">refreshed</option>
          <option value="name">name</option>
        </select>
      </label>
      <button class="sort-dir" onclick={toggleSortDir} title={`Sort: ${sortDirLabel}`} aria-label={`Sort: ${sortDirLabel}`}>
        <Icon path={store.sortDir === 'asc' ? mdiSortAscending : mdiSortDescending} size={15} />
      </button>
    </div>
  </div>

  {#if store.loaded && openAll.length}
    <div class="list-summary">
      {@render pill('open', summary.open, '', 'Show all open pull requests')}
      {#if showPill(summary.caution, 'caution')}
        {@render pill('caution', summary.caution, 'caution', 'Only PRs with cautions')}
      {/if}
      {#if showPill(summary.merged, 'merged')}
        {@render pill('merged', summary.merged, 'merged', 'Only recently merged PRs')}
      {/if}
      {#if expandableRows.length}
        <button
          class="expand-all"
          aria-expanded={allExpanded}
          title={allExpanded ? 'Collapse every row summary' : 'Expand every row summary'}
          onclick={toggleExpandAll}
        >
          <Icon path={allExpanded ? mdiUnfoldLessHorizontal : mdiUnfoldMoreHorizontal} size={14} />
          {allExpanded ? 'Collapse all' : 'Expand all'}
        </button>
      {/if}
    </div>
  {/if}

  {#if !store.loaded}
    <!-- Initial list load is fast; show nothing rather than flash a loader. -->
  {:else if shown.length === 0}
    {#if filterActive}
      <p class="empty">No pull requests match your filter.</p>
    {:else}
      {@render allClear()}
    {/if}
  {:else}
    {#if openPrs.length}
      <ul class="cards">
        {#each openPrs as pr (pr.number)}{@render prCard(pr)}{/each}
      </ul>
    {:else if !filterActive}
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

  <!-- Fixed to the viewport (the list scrolls inside .list-screen), revealed
       once the user is well past the top. -->
  <button class="scroll-top" class:show={scrolled} onclick={scrollToTop} aria-label="Scroll to top" title="Scroll to top">
    <Icon path={mdiArrowUp} size={20} />
  </button>
</div>
