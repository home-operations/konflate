<script lang="ts">
  import type { PRStatus } from './types';
  import {
    store,
    filteredPRs,
    matchesStatus,
    statusFromQuery,
    sortPRs,
    openPR,
    ensurePreview,
    type StatusFilter,
  } from './store.svelte';
  import { router, navigate, replace } from './router.svelte';
  import { paging, setPageSize, parsePageSize, PAGE_SIZES, DEFAULT_PAGE_SIZE, type PageSize } from './paging.svelte';
  import { clock, timeAgo, absolute } from './time.svelte';
  import { slide } from 'svelte/transition';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import Avatar from './Avatar.svelte';
  import Footer from './Footer.svelte';
  import Breakable from './Breakable.svelte';
  import MergeCommand from './MergeCommand.svelte';
  import ForgeLink from './ForgeLink.svelte';
  import Check from './Check.svelte';
  import {
    mdiAlert,
    mdiPackageVariantClosed,
    mdiAlertCircleOutline,
    mdiFileDocumentOutline,
    mdiClockOutline,
    mdiRefresh,
    mdiTagOutline,
    mdiSourceBranch,
    mdiClose,
    mdiFilterOutline,
    mdiSortVariant,
    mdiSortAscending,
    mdiSortDescending,
    mdiChevronDown,
    mdiChevronUp,
    mdiChevronLeft,
    mdiChevronRight,
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
  // The active pill (or default = open, non-hidden) selects what's shown, then
  // the sort orders it. Rendered as one flat list — merged and hidden PRs are
  // reached via their pills, not a separate group. A typed `status:` facet acts
  // like the matching pill, so `status:hidden` / `status:merged` in the search
  // box reveal those sections instead of being re-hidden by the default pill.
  const effectiveStatus = $derived(statusFromQuery(store.query) ?? store.statusFilter);
  const shown = $derived(sortPRs(prs.filter((p) => matchesStatus(p, effectiveStatus))));
  // The full open, non-hidden set — for the default-base heuristic and the pill
  // counts (which hold steady while a pill narrows what's shown).
  const openPrs = $derived(prs.filter((p) => p.open && !p.hidden));
  const filterActive = $derived(store.query.trim() !== '' || store.statusFilter !== '');

  // ---- pagination ---------------------------------------------------------
  // Page size is a persisted display preference (paging.size); the page number is
  // deep-linked in the URL (router '#/page/N'). Both apply to `shown` — the
  // already-filtered, pill-narrowed, sorted set — so the pager's "of N" always
  // equals the active pill's count, and a pill / sort / size change resets to page
  // 1 (resetPage), while a live data update only clamps (the $effect below).
  const size = $derived(paging.size);
  const page = $derived(router.route.name === 'list' ? (router.route.page ?? 1) : 1);
  const totalPages = $derived(size === 'all' ? 1 : Math.max(1, Math.ceil(shown.length / size)));
  const paged = $derived(size === 'all' ? shown : shown.slice((page - 1) * size, page * size));
  // The 1-based [from, to] of the visible window, for the "A–B of N" readout.
  const from = $derived(shown.length === 0 ? 0 : size === 'all' ? 1 : (page - 1) * size + 1);
  const to = $derived(size === 'all' ? shown.length : Math.min(page * size, shown.length));
  // The pager only earns its space once the list outgrows the smallest page size;
  // below that every size shows the same rows, so the controls would be inert.
  const paginated = $derived(shown.length > DEFAULT_PAGE_SIZE);

  // Keep the page in range as the set shrinks under it — a live update dropping
  // rows, or a deep link past the end — instead of stranding the user on a blank
  // page. replace(), so the correction leaves no history entry. Guarded on
  // `loaded`: before the first list fetch resolves, shown is empty and totalPages
  // is 1, which would otherwise clobber a deep-linked #/page/N before its data
  // even arrives.
  $effect(() => {
    if (store.loaded && page > totalPages) replace({ name: 'list', page: totalPages });
  });

  // gotoPage moves to a page (a history entry, so Back walks pages) and returns to
  // the top of the list. resetPage drops to page 1 with no history entry — for
  // filter / sort / size changes, where the prior page no longer means anything.
  function gotoPage(p: number): void {
    navigate({ name: 'list', page: p });
    screenEl?.scrollTo({ top: 0 });
  }
  function resetPage(): void {
    if (page !== 1) replace({ name: 'list', page: 1 });
  }
  function changePageSize(s: PageSize): void {
    setPageSize(s);
    resetPage();
  }

  // At-a-glance health, shown above the list — each pill is also a toggle that
  // filters the list to that status. merged and hidden are pill-only (kept out
  // of the default open view); hidden = excluded by the PR filter, not rendered.
  const summary = $derived.by(() => ({
    open: openPrs.length,
    caution: openPrs.filter((p) => (p.signals?.caution ?? 0) > 0).length,
    failure: openPrs.filter((p) => (p.signals?.failures ?? 0) > 0).length,
    merged: prs.filter((p) => !p.open).length,
    hidden: prs.filter((p) => p.hidden).length,
  }));

  function toggleFilter(f: StatusFilter): void {
    store.statusFilter = store.statusFilter === f ? '' : f;
    resetPage(); // a different subset — restart it at page 1
  }
  // A pill renders while it counts something — or while it's the active
  // filter, so it can't strand itself unclickable when its count hits zero.
  const showPill = (count: number, f: StatusFilter): boolean => count > 0 || store.statusFilter === f;

  // Picking a sort field resets to its natural direction (time newest-first,
  // name A→Z); the direction button then flips it.
  function onSortKeyChange(): void {
    store.sortDir = store.sort === 'name' ? 'asc' : 'desc';
    resetPage();
  }
  function toggleSortDir(): void {
    store.sortDir = store.sortDir === 'asc' ? 'desc' : 'asc';
    resetPage();
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

  // Per-row inline summary: the chevron toggles it, lazy-loading the PR's diff
  // summary on first open (the row click still opens the full review). Keyed by
  // PR number; the data is cached in the store and keyed by headSha there.
  let expanded = $state<Record<number, boolean>>({});
  const isExpanded = (n: number): boolean => !!expanded[n];
  function toggleExpand(n: number, headSha: string): void {
    expanded[n] = !expanded[n];
    if (expanded[n]) ensurePreview(n, headSha);
  }
  // The summary loads async, so on the FIRST open the panel would slide to its
  // loading height and then jump as the fetched sections arrive. Hold the slide
  // until the summary has resolved (a reopen is smooth precisely because the data
  // is already cached), so it animates once, straight to the final height; the
  // chevron spins meanwhile.
  const previewReady = (n: number): boolean => {
    const p = store.previews[n];
    return !!p && p.state !== 'loading';
  };

  // The visible rows that have a summary to toggle. Drives the expand/collapse-all
  // control — scoped to the current page, so "expand all" expands what's on screen
  // and never lazy-loads previews for rows sitting on other pages.
  const expandableRows = $derived(paged.filter((p) => p.signals));
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

<!-- Prev / page indicator / next — shared by the bottom pager and the compact
     top control beside expand-all, so paging a long list doesn't require scrolling
     to its end. The caller guards on totalPages > 1. -->
{#snippet pagerNav()}
  <div class="pager-nav">
    <button
      class="pager-btn"
      onclick={() => gotoPage(page - 1)}
      disabled={page <= 1}
      aria-label="Previous page"
      title="Previous page"
    >
      <Icon path={mdiChevronLeft} size={18} />
    </button>
    <span class="pager-page">Page {page} of {totalPages}</span>
    <button
      class="pager-btn"
      onclick={() => gotoPage(page + 1)}
      disabled={page >= totalPages}
      aria-label="Next page"
      title="Next page"
    >
      <Icon path={mdiChevronRight} size={18} />
    </button>
  </div>
{/snippet}

<!-- The pager sits below the cards once the list outgrows the smallest page. Its
     "of N" counts the active view (the same set the active pill counts), the size
     picker is the persisted per-page preference, and prev/next appear only when
     there's more than one page. -->
{#snippet pager()}
  <nav class="pager" aria-label="Pull request pages">
    <span class="pager-count">
      {#if size === 'all'}
        {shown.length} pull {shown.length === 1 ? 'request' : 'requests'}
      {:else}
        {from}–{to} of {shown.length}
      {/if}
    </span>
    <label class="page-size">
      <span>per page</span>
      <select
        value={String(size)}
        onchange={(e) => changePageSize(parsePageSize(e.currentTarget.value))}
        aria-label="Pull requests per page"
      >
        {#each PAGE_SIZES as s}
          <option value={String(s)}>{s === 'all' ? 'All' : s}</option>
        {/each}
      </select>
    </label>
    {#if totalPages > 1}{@render pagerNav()}{/if}
  </nav>
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

  <!-- The sliding panel only mounts once the summary has loaded (see
       previewReady), so there's no in-panel "loading…" height for the slide to
       animate through and then jump past — the chevron carries the spinner. -->
  {#if pv && pv.state === 'pending'}
    <div class="pv-msg"><Icon path={mdiTrayFull} size={14} /> Still rendering — open the PR to watch.</div>
  {:else if pv && pv.state === 'error'}
    <div class="pv-msg pv-error"><Icon path={mdiAlertCircleOutline} size={14} /> {pv.error}</div>
  {:else if pv}
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
  <!-- The squared-bottom "expanded" look is tied to the panel actually being
       shown (not just the toggle), so a card never flattens its corners while the
       summary is still loading and no panel hangs below it yet. -->
  <li class="card-li" class:expanded={isExpanded(pr.number) && previewReady(pr.number)}>
    <div
      class="card-shell"
      data-pr={pr.number}
      class:merged={!pr.open}
      class:caution={pr.open && (pr.signals?.caution ?? 0) > 0}
      class:failure={pr.open && (pr.signals?.failures ?? 0) > 0}
    >
      <button class="card" onclick={() => openPR(pr.number)}>
        <div class="card-top">
        <span class="dot {pr.hidden ? 'dot-hidden' : pr.open ? `dot-${pr.status}` : 'dot-merged'}"></span>
        <span class="card-title"><Breakable text={pr.title} /></span>
        {#if pr.checks}<Check checks={pr.checks} />{/if}
      </div>
      <div class="card-meta">
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
            {#if pr.signals.failures}
              <span class="badge danger" title="render failures"><Icon path={mdiAlertCircleOutline} size={13} /> {pr.signals.failures}</span>
            {/if}
            {#if pr.signals.caution}
              <span class="badge caution" title="cautions"><Icon path={mdiAlert} size={13} /> {pr.signals.caution}</span>
            {/if}
            {#if pr.signals.images}
              <span class="badge" title={`${pr.signals.images} image change${pr.signals.images === 1 ? '' : 's'}`}><Icon path={mdiPackageVariantClosed} size={13} /> {pr.signals.images}</span>
            {/if}
            <span class="badge muted" title={`${pr.signals.resources} resource change${pr.signals.resources === 1 ? '' : 's'}`}>
              <Icon path={mdiFileDocumentOutline} size={13} /> {pr.signals.resources}
            </span>
          </span>
        {/if}
      </div>
      </button>
      <!-- Icon-only link out to the PR on its forge (the number would just crowd
           the row — it stays in the link's tooltip/aria-label). Sibling of the
           card <button> (it can't nest an anchor), right of the resource count. -->
      <ForgeLink url={pr.url} number={pr.number} showNumber={false} />
      <!-- Right column: the expand chevron once a PR has a rendered summary;
           otherwise a state icon — rendering / queued / failed — carried by the
           icon and its tooltip, no text (so the row stays compact, incl. mobile). -->
      {#if pr.signals}
        <button
          class="card-expand"
          aria-expanded={isExpanded(pr.number)}
          aria-label={isExpanded(pr.number) ? `Hide summary for #${pr.number}` : `Show summary for #${pr.number}`}
          onclick={() => toggleExpand(pr.number, pr.headSha)}
        >
          {#if isExpanded(pr.number) && !previewReady(pr.number)}
            <Spinner size={16} />
          {:else}
            <Icon path={isExpanded(pr.number) ? mdiChevronUp : mdiChevronDown} size={18} />
          {/if}
        </button>
      {:else if pr.open && !pr.hidden && (pr.status === 'running' || pr.status === 'pending')}
        <span
          class="card-state"
          title={pr.status === 'running' ? 'Rendering…' : 'Queued to render'}
          aria-label={pr.status === 'running' ? 'Rendering' : 'Queued to render'}
        >
          {#if pr.status === 'running'}<Spinner size={16} />{:else}<Icon path={mdiTrayFull} size={16} />{/if}
        </span>
      {:else if pr.open && !pr.hidden && pr.status === 'error'}
        <span class="card-state error" title={pr.error ? `Render failed: ${pr.error}` : 'Render failed'} aria-label="Render failed">
          <Icon path={mdiAlertCircleOutline} size={18} />
        </span>
      {:else}
        <!-- Reserve the disclosure column's width so the forge link aligns into a
             column across rows whether or not a PR has rendered a summary yet. -->
        <span class="card-expand-spacer" aria-hidden="true"></span>
      {/if}
    </div>
    {#if isExpanded(pr.number) && previewReady(pr.number)}
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
        oninput={resetPage}
        aria-label="Filter pull requests"
      />
      {#if store.query}
        <button
          class="clear-btn"
          onclick={() => {
            store.query = '';
            resetPage();
          }}
          aria-label="Clear filter"
        >
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

  {#if store.loaded && (summary.open || summary.merged || summary.hidden)}
    <div class="list-summary">
      {@render pill('open', summary.open, '', 'Show open pull requests')}
      {#if showPill(summary.failure, 'failure')}
        {@render pill('failure', summary.failure, 'failure', 'Only PRs that failed to render')}
      {/if}
      {#if showPill(summary.caution, 'caution')}
        {@render pill('caution', summary.caution, 'caution', 'Only PRs with cautions')}
      {/if}
      {#if showPill(summary.merged, 'merged')}
        {@render pill('merged', summary.merged, 'merged', 'Only recently merged PRs')}
      {/if}
      {#if showPill(summary.hidden, 'hidden')}
        {@render pill('hidden', summary.hidden, 'hidden', 'PRs excluded by the filter — listed but not rendered')}
      {/if}
      {#if expandableRows.length || (paginated && totalPages > 1)}
        <div class="list-summary-end">
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
          <!-- A compact prev/next beside expand-all so paging is reachable from the
               top of a long list; the full pager (count + size) stays at the bottom. -->
          {#if paginated && totalPages > 1}
            {@render pagerNav()}
          {/if}
        </div>
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
    <ul class="cards">
      {#each paged as pr (pr.number)}{@render prCard(pr)}{/each}
    </ul>
    {#if paginated}{@render pager()}{/if}
  {/if}

  <Footer />

  <!-- Fixed to the viewport (the list scrolls inside .list-screen), revealed
       once the user is well past the top. -->
  <button class="scroll-top" class:show={scrolled} onclick={scrollToTop} aria-label="Scroll to top" title="Scroll to top">
    <Icon path={mdiArrowUp} size={20} />
  </button>
</div>
