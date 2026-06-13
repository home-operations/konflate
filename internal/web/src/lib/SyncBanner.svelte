<script lang="ts">
  import { mdiAlertOutline } from '@mdi/js';
  import Icon from './Icon.svelte';
  import { store } from './store.svelte';

  // Non-null only when the last forge poll failed (rate-limited or unreachable);
  // see the store's `sync` and the "sync" websocket event.
  const sync = $derived(store.sync);

  // A "resets in ~Nm" countdown for a rate-limit reset (retryAt is Unix seconds).
  // The 30s tick refreshes the minute-granularity label while the banner is up;
  // it only runs when there's a reset time to show, so a healthy app idles.
  let now = $state(Math.floor(Date.now() / 1000));
  $effect(() => {
    if (!sync || sync.ok || !sync.retryAt) return;
    const id = setInterval(() => (now = Math.floor(Date.now() / 1000)), 30_000);
    return () => clearInterval(id);
  });

  function resetsIn(retryAt: number): string {
    const secs = retryAt - now;
    if (secs <= 0) return 'momentarily';
    const m = Math.ceil(secs / 60);
    return m === 1 ? '~1 minute' : `~${m} minutes`;
  }
</script>

{#if sync && !sync.ok}
  <div class="sync-banner" role="status" aria-live="polite">
    <Icon path={mdiAlertOutline} size={16} />
    <span class="sync-banner-text">
      Couldn’t list pull requests — {sync.message ?? 'the forge is unreachable.'}
      {#if sync.reason === 'rate_limited'}
        {#if sync.retryAt}<strong>Resets in {resetsIn(sync.retryAt)}.</strong>{/if}
        Configure a forge token or GitHub App to raise the API rate limit.
      {:else}
        Any pull requests already loaded are still shown below.
      {/if}
    </span>
  </div>
{/if}
