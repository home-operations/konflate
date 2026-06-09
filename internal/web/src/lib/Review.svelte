<script lang="ts">
  import { router } from './router.svelte';
  import { store, currentPR, goList, adjacentPR } from './store.svelte';
  import { clock, timeAgo, absolute } from './time.svelte';
  import Icon from './Icon.svelte';
  import Avatar from './Avatar.svelte';
  import Breakable from './Breakable.svelte';
  import {
    mdiArrowLeft,
    mdiChevronLeft,
    mdiChevronRight,
    mdiSourceMerge,
    mdiTrayFull,
    mdiClockOutline,
    mdiRefresh,
    mdiFilterOutline,
  } from './icons';
  import Diffs from './Diffs.svelte';
  import Copy from './Copy.svelte';
  import ForgeLink from './ForgeLink.svelte';

  const route = $derived(router.route.name === 'review' ? router.route : null);
  const pr = $derived(currentPR());
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
        <span class="rt-name"><Breakable text={pr?.title ?? ''} /></span>
        {#if pr}<ForgeLink url={pr.url} number={pr.number} size={16} />{/if}
        <div class="rt-meta">
          {#if pr}
            <span class="rt-tag rt-author"><Avatar src={pr.authorAvatar} size={16} /> {pr.author}</span>
            <span class="rt-tag sha-wrap">
              <code class="sha">{pr.headSha.slice(0, 7)}</code>
              <Copy text={pr.headSha} label="Copy full commit SHA" />
            </span>
            {#if pr.createdAt}
              <span class="rt-tag ago" title={`Opened ${absolute(pr.createdAt)}`}><Icon path={mdiClockOutline} size={13} /> {timeAgo(pr.createdAt, clock.now)}</span>
            {/if}
            {#if pr.updatedAt}
              <span class="rt-tag ago" title={`Last rendered ${absolute(pr.updatedAt)}`}><Icon path={mdiRefresh} size={13} /> {timeAgo(pr.updatedAt, clock.now)}</span>
            {/if}
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

    <div class="review-body">
      {#if store.diffHidden}
        <!-- Excluded by the PR filter: konflate lists this PR but never renders
             it (so a fork's untrusted code never runs), so there's no diff. -->
        <div class="loading-center">
          <Icon path={mdiFilterOutline} size={38} />
          <p>Excluded by the PR filter — konflate lists this PR but doesn't render it.</p>
        </div>
      {:else if store.diffError}
        <p class="error-box">{store.diffError}</p>
      {:else if store.diff}
        <Diffs />
      {:else if store.loading && (pr?.status === 'pending' || pr?.status === 'running')}
        <!-- A genuine server-side render (not a quick fetch): a status message,
             never a spinner — it can take a moment, and this isn't a fast-load
             flash. -->
        <div class="loading-center">
          <Icon path={mdiTrayFull} size={38} />
          <p>{pr?.status === 'pending' ? 'Queued — waiting to render…' : 'Rendering the diff…'}</p>
        </div>
      {:else if store.loading}
        <!-- Fetching an already-rendered diff: keep the pane blank — it resolves
             fast, so any loader would only flash. -->
      {:else}
        <p class="empty">No diff available.</p>
      {/if}
    </div>
  </div>
{/if}
