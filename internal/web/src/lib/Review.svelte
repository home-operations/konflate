<script lang="ts">
  import { router } from './router.svelte';
  import { store, currentPR, goList, setTab, adjacentPR } from './store.svelte';
  import { viewedCount } from './viewed.svelte';
  import { clock, timeAgo, absolute } from './time.svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import Avatar from './Avatar.svelte';
  import {
    mdiArrowLeft,
    mdiChevronLeft,
    mdiChevronRight,
    mdiAlertOctagon,
    mdiOpenInNew,
    mdiSourceMerge,
    mdiTrayFull,
    mdiClockOutline,
  } from './icons';
  import Overview from './Overview.svelte';
  import Diffs from './Diffs.svelte';
  import Copy from './Copy.svelte';

  const route = $derived(router.route.name === 'review' ? router.route : null);
  const pr = $derived(currentPR());
  const forgeUrl = $derived(pr && /^https?:\/\//i.test(pr.url) ? pr.url : null);
  const merged = $derived(pr ? !pr.open : false);
  const danger = $derived(store.diff?.warnings?.filter((w) => w.level === 'danger') ?? []);
  const total = $derived(store.diff?.resources?.length ?? 0);
  const seen = $derived(pr ? viewedCount(pr.number, pr.headSha) : 0);
</script>

{#if route}
  <div class="review">
    <div class="review-head">
      <button class="btn btn-icon" onclick={goList} title="Back to list (Esc)">
        <Icon path={mdiArrowLeft} label="Back to list" />
      </button>
      <div class="review-title">
        <div class="rt-line">
          <span class="pr-num">#{route.pr}</span>
          <span class="rt-name">{pr?.title ?? ''}</span>
        </div>
        <div class="rt-meta">
          {#if pr}
            <span class="sha-wrap">
              <code class="sha">{pr.headSha.slice(0, 7)}</code>
              <Copy text={pr.headSha} label="Copy full commit SHA" />
            </span>
            <span class="rt-author"><Avatar src={pr.authorAvatar} size={16} /> {pr.author}</span>
            {#if pr.updatedAt}
              <span class="ago" title={`Last refreshed ${absolute(pr.updatedAt)}`}><Icon path={mdiClockOutline} size={13} /> {timeAgo(pr.updatedAt, clock.now)}</span>
            {/if}
          {/if}
          {#if forgeUrl}
            <a class="ext" href={forgeUrl} target="_blank" rel="noopener noreferrer">
              <Icon path={mdiOpenInNew} size={13} /> open
            </a>
          {/if}
        </div>
      </div>
      <div class="review-nav">
        {#if total}<span class="progress">{seen}/{total} viewed</span>{/if}
        <button class="btn btn-icon" onclick={() => adjacentPR(-1)} title="Previous PR ([)">
          <Icon path={mdiChevronLeft} label="Previous PR" />
        </button>
        <button class="btn btn-icon" onclick={() => adjacentPR(1)} title="Next PR (])">
          <Icon path={mdiChevronRight} label="Next PR" />
        </button>
      </div>
    </div>

    {#if merged}
      <div class="merged-strip">
        <Icon path={mdiSourceMerge} size={15} /> Merged — diff frozen at merge time
      </div>
    {/if}

    {#if danger.length}
      <div class="danger-strip">
        <Icon path={mdiAlertOctagon} size={15} />
        {danger.length} danger {danger.length === 1 ? 'warning' : 'warnings'} — {danger[0].resource}{danger.length >
        1
          ? ` and ${danger.length - 1} more`
          : ''}
      </div>
    {/if}

    <div class="tabs">
      <button class:active={route.tab === 'overview'} onclick={() => setTab('overview')}>Overview</button>
      <button class:active={route.tab === 'diffs'} onclick={() => setTab('diffs')}>
        Diffs{#if total}<span class="count">{total}</span>{/if}
      </button>
    </div>

    <div class="review-body" class:full={route.tab === 'diffs'}>
      {#if store.loading || pr?.status === 'running'}
        <div class="loading-center"><Spinner size={46} label="Rendering" /><p>Rendering the diff…</p></div>
      {:else if pr?.status === 'pending'}
        <div class="loading-center"><Icon path={mdiTrayFull} size={38} /><p>Queued — waiting to render…</p></div>
      {:else if store.diffError}
        <p class="error-box">{store.diffError}</p>
      {:else if !store.diff}
        <p class="empty">No diff available.</p>
      {:else if route.tab === 'overview'}
        <Overview />
      {:else}
        <Diffs />
      {/if}
    </div>
  </div>
{/if}
