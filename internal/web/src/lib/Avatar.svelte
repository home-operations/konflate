<script lang="ts">
  import Icon from './Icon.svelte';
  import { mdiAccountOutline } from './icons';

  // src is the same-origin /api/avatar proxy path (or absent). On a load error
  // (avatar 404 / private-forge auth) we fall back to the person icon, so the
  // UI degrades gracefully and never shows a broken image.
  let { src, size = 14 }: { src?: string; size?: number } = $props();
  let failed = $state(false);
</script>

{#if src && !failed}
  <img class="avatar" {src} alt="" width={size} height={size} loading="lazy" onerror={() => (failed = true)} />
{:else}
  <Icon path={mdiAccountOutline} size={size} />
{/if}
