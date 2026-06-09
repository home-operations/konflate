<script lang="ts">
  // The PR's number as a link out to the change on its forge, drawn with a
  // pull-request glyph. The tooltip names the forge (GitHub / GitLab / Forgejo)
  // from the instance meta. This is the one place the PR number is shown — both
  // the list rows and the review title render it here, right of the signals.
  import Icon from './Icon.svelte';
  import { store } from './store.svelte';
  import { mdiSourcePull, forgeIcon } from './icons';

  interface Props {
    url: string;
    number: number;
    size?: number;
  }
  let { url, number, size = 14 }: Props = $props();

  // Only link an absolute http(s) url; otherwise still show the number, unlinked.
  const valid = $derived(/^https?:\/\//i.test(url));
  // The forge's display name ("GitHub"/"GitLab"/"Forgejo") from the brand-icon
  // registry, falling back to a neutral noun if meta hasn't loaded.
  const forgeName = $derived(forgeIcon[store.meta?.forge ?? '']?.title ?? 'the forge');
</script>

{#if valid}
  <a
    class="forge-link"
    href={url}
    target="_blank"
    rel="noopener noreferrer"
    title={`Open PR #${number} on ${forgeName}`}
    aria-label={`Open PR #${number} on ${forgeName}`}
  >
    <Icon path={mdiSourcePull} {size} /> #{number}
  </a>
{:else}
  <span class="forge-link forge-link-static"><Icon path={mdiSourcePull} {size} /> #{number}</span>
{/if}
