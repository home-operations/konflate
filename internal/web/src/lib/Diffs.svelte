<script lang="ts">
  import { router } from './router.svelte';
  import { store, selectedResource, openDiffs } from './store.svelte';
  import Tree from './Tree.svelte';
  import Diff from './Diff.svelte';

  const res = $derived(selectedResource());

  // Entering Diffs without a resource (from the tab or a deep link) selects the
  // first one once the diff has loaded.
  $effect(() => {
    const r = router.route;
    if (r.name === 'review' && r.tab === 'diffs' && !r.resource) {
      const first = store.diff?.resources?.[0];
      if (first) openDiffs(r.pr, first.id);
    }
  });
</script>

<div class="diffs">
  <aside class="rail"><Tree /></aside>
  <div class="diff-pane">
    {#if res}
      <Diff resource={res} />
    {:else}
      <p class="empty">Select a resource from the list.</p>
    {/if}
  </div>
</div>
