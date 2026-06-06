<script lang="ts">
  import { router } from './router.svelte';
  import { store, currentPR, goList, adjacentPR } from './store.svelte';
  import { clock, timeAgo, absolute } from './time.svelte';
  import Icon from './Icon.svelte';
  import Smasher from './Smasher.svelte';
  import Avatar from './Avatar.svelte';
  import {
    mdiArrowLeft,
    mdiChevronLeft,
    mdiChevronRight,
    mdiOpenInNew,
    mdiSourceMerge,
    mdiSourcePull,
    mdiTrayFull,
    mdiClockOutline,
    mdiRefresh,
    mdiConsoleLine,
  } from './icons';
  import Diffs from './Diffs.svelte';
  import Copy from './Copy.svelte';

  const route = $derived(router.route.name === 'review' ? router.route : null);
  const pr = $derived(currentPR());
  const forgeUrl = $derived(pr && /^https?:\/\//i.test(pr.url) ? pr.url : null);
  const merged = $derived(pr ? !pr.open : false);
</script>

{#if route}
  <div class="review">
    <div class="review-head">
      <!-- All navigation clusters on the left (back, then prev/next) so the
           row ends with the title instead of orphan chevrons at the far right. -->
      <button class="btn btn-icon" onclick={goList} title="Back to list (Esc)">
        <Icon path={mdiArrowLeft} label="Back to list" />
      </button>
      <div class="review-nav">
        <button class="btn btn-icon" onclick={() => adjacentPR(-1)} title="Previous PR ([)">
          <Icon path={mdiChevronLeft} label="Previous PR" />
        </button>
        <button class="btn btn-icon" onclick={() => adjacentPR(1)} title="Next PR (])">
          <Icon path={mdiChevronRight} label="Next PR" />
        </button>
      </div>
      <div class="review-title">
        <div class="rt-line">
          <span class="rt-name">{pr?.title ?? ''}</span>
        </div>
        <div class="rt-meta">
          <span class="rt-tag pr-id"><Icon path={mdiSourcePull} size={13} /> #{route.pr}</span>
          {#if pr}
            <span class="rt-tag rt-author"><Avatar src={pr.authorAvatar} size={16} /> {pr.author}</span>
            <span class="rt-tag sha-wrap">
              <code class="sha">{pr.headSha.slice(0, 7)}</code>
              <Copy text={pr.headSha} label="Copy full commit SHA" />
            </span>
            {#if pr.createdAt}
              <span class="rt-tag ago" title={`Opened ${absolute(pr.createdAt)}`}><Icon path={mdiClockOutline} size={13} /> opened {timeAgo(pr.createdAt, clock.now)}</span>
            {/if}
            {#if pr.updatedAt}
              <span class="rt-tag ago" title={`Last rendered ${absolute(pr.updatedAt)}`}><Icon path={mdiRefresh} size={13} /> refreshed {timeAgo(pr.updatedAt, clock.now)}</span>
            {/if}
          {/if}
          {#if forgeUrl}
            <a class="rt-tag ext" href={forgeUrl} target="_blank" rel="noopener noreferrer">
              <Icon path={mdiOpenInNew} size={13} /> open
            </a>
          {/if}
        </div>
      </div>
    </div>

    {#if merged}
      <div class="merged-strip">
        <Icon path={mdiSourceMerge} size={15} /> Merged — diff frozen at merge time
      </div>
    {/if}

    {#if store.diffRefreshError}
      <div class="refresh-strip" title={store.diffRefreshError}>
        <Icon path={mdiRefresh} size={15} /> Couldn't refresh — showing the last successful render
      </div>
    {/if}

    {#if store.diffMergeCommand}
      <div class="merge-cmd">
        <Icon path={mdiConsoleLine} size={14} />
        <code>{store.diffMergeCommand}</code>
        <Copy text={store.diffMergeCommand} label="Copy merge command" />
      </div>
    {/if}

    <div class="review-body">
      {#if store.diffError}
        <p class="error-box">{store.diffError}</p>
      {:else if store.diff}
        <Diffs />
      {:else if store.loadingSlow}
        {#if pr?.status === 'pending'}
          <div class="loading-center"><Icon path={mdiTrayFull} size={38} /><p>Queued — waiting to render…</p></div>
        {:else}
          <div class="loading-center"><Smasher size={130} /><p>Rendering the diff…</p></div>
        {/if}
      {:else if store.loading}
        <!-- Brief pre-spinner window: keep the pane blank so a fast load (an
             already-rendered diff, or a reload) never flashes the spinner. -->
      {:else}
        <p class="empty">No diff available.</p>
      {/if}
    </div>
  </div>
{/if}
