<script lang="ts">
  import { router } from './router.svelte';
  import { store, adjacentResource, selectables } from './store.svelte';
  import Tree from './Tree.svelte';
  import Diff from './Diff.svelte';
  import Overview from './Overview.svelte';
  import Icon from './Icon.svelte';
  import { mdiChevronLeft, mdiChevronRight } from './icons';

  // The selection: 'summary', a resource id, or null. A bare #/pr/N (null) lands
  // on the Summary — the first tree node — so the URL stays clean and a reviewer
  // sees the impact/warnings before drilling into a diff.
  const sel = $derived(router.route.name === 'review' ? (router.route.sel ?? 'summary') : 'summary');
  const res = $derived(
    sel === 'summary' ? null : (store.diff?.resources?.find((r) => r.id === sel) ?? null),
  );

  // [Summary, ...resources] — the order the switcher and j/k step through.
  const ids = $derived(selectables());
  const idx = $derived(Math.max(0, ids.indexOf(sel)));
  const switchLabel = $derived(sel === 'summary' ? 'Summary' : (res?.title ?? sel));
</script>

<div class="diffs">
  <aside class="rail"><Tree /></aside>
  <div class="diff-main">
    <!-- The tree rail is hidden on narrow screens; this bar is the navigator
         there, cycling Summary + every resource. -->
    <div class="diff-switcher">
      <button
        class="btn btn-icon"
        onclick={() => adjacentResource(-1)}
        disabled={idx <= 0}
        title="Previous (k)"
        aria-label="Previous"
      >
        <Icon path={mdiChevronLeft} size={16} />
      </button>
      <span class="switcher-label">
        <span class="switcher-pos">{idx + 1}/{ids.length}</span>
        <span class="switcher-name">{switchLabel}</span>
      </span>
      <button
        class="btn btn-icon"
        onclick={() => adjacentResource(1)}
        disabled={idx >= ids.length - 1}
        title="Next (j)"
        aria-label="Next"
      >
        <Icon path={mdiChevronRight} size={16} />
      </button>
    </div>
    <div class="diff-pane">
      {#if sel === 'summary'}
        <Overview />
      {:else if res}
        <Diff resource={res} />
      {:else}
        <p class="empty">Select a resource from the list.</p>
      {/if}
    </div>
  </div>
</div>
