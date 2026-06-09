<script lang="ts">
  // A quiet link out to the pull request on its forge, drawn with a
  // pull-request glyph. The tooltip names the forge (GitHub / GitLab / Forgejo)
  // from the instance meta. Shared by the PR list rows and the review title.
  import Icon from './Icon.svelte';
  import { store } from './store.svelte';
  import { mdiSourcePull, forgeIcon } from './icons';

  interface Props {
    url: string;
    size?: number;
  }
  let { url, size = 15 }: Props = $props();

  // Only render an absolute http(s) link; a missing/relative url is dropped
  // rather than emitting a dead anchor.
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
    title={`Open PR on ${forgeName}`}
    aria-label={`Open PR on ${forgeName}`}
  >
    <Icon path={mdiSourcePull} {size} />
  </a>
{/if}
