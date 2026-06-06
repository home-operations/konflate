<script lang="ts">
  import { onMount } from 'svelte';
  import { router, initRouter } from './lib/router.svelte';
  import { store, loadPRs, loadMeta, connectWS, ensureDiff } from './lib/store.svelte';
  import { theme, cycleTheme, initTheme } from './lib/theme.svelte';
  import { initClock } from './lib/time.svelte';
  import { initKeyboard, help, toggleHelp } from './lib/keyboard.svelte';
  import {
    mdiThemeLightDark,
    mdiWeatherNight,
    mdiWhiteBalanceSunny,
    mdiClockOutline,
    mdiKeyboardOutline,
    mdiOpenInNew,
    forgeIcon,
  } from './lib/icons';
  import Icon from './lib/Icon.svelte';
  import List from './lib/List.svelte';
  import Review from './lib/Review.svelte';
  import type { Meta } from './lib/types';

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

  // Move focus into the help dialog when it opens (screen readers announce it;
  // Escape works from anywhere via the global key handler).
  function focusOnMount(node: HTMLElement): void {
    node.focus();
  }
</script>

<div class="app">
  <header class="topbar">
    <a class="brand" href="#/">
      <img src="/favicon.svg" width="22" height="22" alt="" />
      <span class="wordmark">konflate</span>
      <span class="conn" class:on={store.connected} title={store.connected ? 'live' : 'reconnecting…'}></span>
    </a>

    {#snippet repoChip(meta: Meta)}
      {#if forge}<Icon path={forge.path} label={meta.forge} size={15} />{/if}
      <span>{meta.repo}</span>
    {/snippet}

    {#if store.meta}
      {#if store.meta.repoUrl}
        <a
          class="repo"
          href={store.meta.repoUrl}
          target="_blank"
          rel="noopener noreferrer"
          title={`Open ${store.meta.repo} on ${store.meta.forge}`}
        >
          {@render repoChip(store.meta)}
          <span class="repo-ext"><Icon path={mdiOpenInNew} size={12} /></span>
        </a>
      {:else}
        <div class="repo">{@render repoChip(store.meta)}</div>
      {/if}
    {:else}
      <div class="spacer"></div>
    {/if}

    <div class="actions">
      {#if autoLabel}
        <span class="auto" title={`Pull requests auto-update every ${autoLabel} (plus on webhook)`}>
          <Icon path={mdiClockOutline} size={15} /> <span class="auto-text">auto · {autoLabel}</span>
        </span>
      {/if}
      <!-- Hidden on phones (see the mobile block). -->
      <button class="btn btn-icon kbd-btn" onclick={toggleHelp} title="Keyboard shortcuts (?)">
        <Icon path={mdiKeyboardOutline} label="Keyboard shortcuts" />
      </button>
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

  {#if help.open}
    <!-- The backdrop is a real button so closing is keyboard-reachable. -->
    <div class="help-overlay">
      <button class="help-backdrop" aria-label="Close keyboard shortcuts" onclick={toggleHelp}></button>
      <div class="help-card" role="dialog" aria-label="Keyboard shortcuts" tabindex="-1" use:focusOnMount>
        <h2>Keyboard shortcuts</h2>
        <dl class="help-keys">
          <dt><kbd>j</kbd> / <kbd>k</kbd></dt>
          <dd>next / previous resource</dd>
          <dt><kbd>]</kbd> / <kbd>[</kbd></dt>
          <dd>next / previous pull request</dd>
          <dt><kbd>o</kbd></dt>
          <dd>jump to the summary</dd>
          <dt><kbd>u</kbd> or <kbd>Esc</kbd></dt>
          <dd>back to the list</dd>
          <dt><kbd>/</kbd></dt>
          <dd>filter the list</dd>
          <dt><kbd>?</kbd></dt>
          <dd>toggle this help</dd>
        </dl>
      </div>
    </div>
  {/if}
</div>
