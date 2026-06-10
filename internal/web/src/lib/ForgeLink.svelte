<script lang="ts">
  // The PR's number as a link out to the change on its forge, drawn with a
  // pull-request glyph. The tooltip/aria-label always name the PR number and the
  // forge (GitHub / GitLab / Forgejo) from the instance meta. showNumber toggles
  // the visible "#<n>" text: the review title shows it, while the list rows pass
  // showNumber={false} for an icon-only link (the number stays in the tooltip).
  import Icon from './Icon.svelte';
  import { store } from './store.svelte';
  import { mdiSourcePull, forgeIcon } from './icons';

  interface Props {
    url: string;
    number: number;
    size?: number;
    showNumber?: boolean;
  }
  let { url, number, size = 14, showNumber = true }: Props = $props();

  // Only link an absolute http(s) url; otherwise still show the glyph, unlinked.
  const valid = $derived(/^https?:\/\//i.test(url));
  // The forge's display name ("GitHub"/"GitLab"/"Forgejo") from the brand-icon
  // registry, falling back to a neutral noun if meta hasn't loaded.
  const forgeName = $derived(forgeIcon[store.meta?.forge ?? '']?.title ?? 'the forge');
</script>

{#if valid}
  <a
    class="forge-link"
    class:icon-only={!showNumber}
    href={url}
    target="_blank"
    rel="noopener noreferrer"
    title={`Open PR #${number} on ${forgeName}`}
    aria-label={`Open PR #${number} on ${forgeName}`}
  >
    <Icon path={mdiSourcePull} {size} />{#if showNumber} #{number}{/if}
  </a>
{:else}
  <span class="forge-link forge-link-static" class:icon-only={!showNumber}
    ><Icon path={mdiSourcePull} {size} />{#if showNumber} #{number}{/if}</span
  >
{/if}
