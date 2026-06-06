<script lang="ts">
  // Project footer for the list screen (non-sticky — it scrolls with the
  // cards): the konflate GitHub link (moved out of the topbar), the
  // home-operations Discord, the license, and the running version from
  // /api/meta. The review screens stay footer-free; their chrome is precious.
  import { store } from './store.svelte';
  import Icon from './Icon.svelte';
  import {
    mdiScaleBalance,
    githubMark,
    discordMark,
    KONFLATE_REPO_URL,
    DISCORD_URL,
    LICENSE_URL,
  } from './icons';

  // "0.1.8" → "v0.1.8"; local builds report "dev" and show as-is.
  const version = $derived.by(() => {
    const v = store.meta?.version ?? '';
    return v && v !== 'dev' ? `v${v}` : v;
  });
  const year = new Date().getFullYear();
</script>

<footer class="footer">
  <a class="foot-link" href={KONFLATE_REPO_URL} target="_blank" rel="noopener noreferrer">
    <Icon path={githubMark.path} size={14} /> konflate {version}
  </a>
  <span class="foot-sep">·</span>
  <a class="foot-link" href={DISCORD_URL} target="_blank" rel="noopener noreferrer">
    <Icon path={discordMark.path} size={14} /> Discord
  </a>
  <span class="foot-sep">·</span>
  <a class="foot-link" href={LICENSE_URL} target="_blank" rel="noopener noreferrer">
    <Icon path={mdiScaleBalance} size={14} /> AGPL-3.0
  </a>
  <span class="foot-sep">·</span>
  <span>© {year} home-operations</span>
</footer>
