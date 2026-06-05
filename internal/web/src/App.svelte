<script lang="ts">
  import { onMount } from 'svelte';
  import { router, initRouter } from './lib/router.svelte';
  import { store, loadPRs, loadMeta, connectWS, ensureDiff } from './lib/store.svelte';
  import { theme, cycleTheme, initTheme } from './lib/theme.svelte';
  import { initClock } from './lib/time.svelte';
  import { initKeyboard } from './lib/keyboard.svelte';
  import {
    mdiThemeLightDark,
    mdiWeatherNight,
    mdiWhiteBalanceSunny,
    mdiClockOutline,
    forgeIcon,
    githubMark,
    KONFLATE_REPO_URL,
  } from './lib/icons';
  import Icon from './lib/Icon.svelte';
  import List from './lib/List.svelte';
  import Review from './lib/Review.svelte';

  onMount(() => {
    initTheme();
    initClock();
    initRouter();
    initKeyboard();
    void loadMeta();
    void loadPRs();
    connectWS();
  });

  // Load the diff whenever the route points at a review (idempotent).
  $effect(() => {
    if (router.route.name === 'review') ensureDiff(router.route.pr);
  });

  const themeIconPath = $derived(
    theme.pref === 'auto' ? mdiThemeLightDark : theme.pref === 'dark' ? mdiWeatherNight : mdiWhiteBalanceSunny,
  );
  const forge = $derived(store.meta ? forgeIcon[store.meta.forge] : null);

  // "1800" → "30m". konflate auto-refreshes on this interval; there is no
  // manual refresh trigger.
  function fmtInterval(sec: number): string {
    if (sec <= 0) return '';
    if (sec % 3600 === 0) return `${sec / 3600}h`;
    if (sec % 60 === 0) return `${sec / 60}m`;
    return `${sec}s`;
  }
  const autoLabel = $derived(store.meta ? fmtInterval(store.meta.refreshIntervalSeconds) : '');
</script>

<div class="app">
  <header class="topbar">
    <a class="brand" href="#/">
      <img src="/favicon.svg" width="22" height="22" alt="" />
      <span>konflate</span>
      <span class="conn" class:on={store.connected} title={store.connected ? 'live' : 'reconnecting…'}></span>
    </a>

    {#if store.meta}
      <div class="repo">
        {#if forge}<Icon path={forge.path} label={store.meta.forge} size={15} />{/if}
        <span>{store.meta.repo}</span>
      </div>
    {:else}
      <div class="spacer"></div>
    {/if}

    <div class="actions">
      {#if autoLabel}
        <span class="auto" title={`Pull requests auto-update every ${autoLabel} (plus on webhook)`}>
          <Icon path={mdiClockOutline} size={15} /> auto · {autoLabel}
        </span>
      {/if}
      <a
        class="btn btn-icon gh"
        href={KONFLATE_REPO_URL}
        target="_blank"
        rel="noopener noreferrer"
        title="konflate on GitHub"
        aria-label="konflate on GitHub"
      >
        <Icon path={githubMark.path} size={16} />
      </a>
      <button class="btn btn-icon" onclick={cycleTheme} title={`Theme: ${theme.pref}`}>
        <Icon path={themeIconPath} label="Toggle theme" />
      </button>
    </div>
  </header>

  {#if router.route.name === 'review'}
    <Review />
  {:else}
    <List />
  {/if}
</div>
