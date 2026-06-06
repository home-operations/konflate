<script lang="ts">
  import { router, replace } from './router.svelte';
  import { store, adjacentResource, selectables } from './store.svelte';
  import Tree from './Tree.svelte';
  import Diff from './Diff.svelte';
  import Overview from './Overview.svelte';
  import Icon from './Icon.svelte';
  import { mdiChevronLeft, mdiChevronRight } from './icons';

  // The selection: 'summary' or a resource id. A bare #/pr/N (null) lands on
  // the Summary. Every section renders stacked in one scrolling pane — the
  // selection is a scroll position, not a swap: the tree (and j/k, and the
  // mobile switcher) scroll to a section, and scrolling updates the selection.
  const sel = $derived(router.route.name === 'review' ? (router.route.sel ?? 'summary') : 'summary');
  const resources = $derived(store.diff?.resources ?? []);
  const res = $derived(sel === 'summary' ? null : (resources.find((r) => r.id === sel) ?? null));

  // [Summary, ...resources] — the order the switcher and j/k step through.
  const ids = $derived(selectables());
  const idx = $derived(Math.max(0, ids.indexOf(sel)));
  const switchLabel = $derived(sel === 'summary' ? 'Summary' : (res?.title ?? sel));

  let pane = $state<HTMLElement | null>(null);

  // The selection the scrollspy last derived. Navigation (tree click, j/k,
  // deep link) only scrolls when the route disagrees with it, so scroll-driven
  // route updates never re-scroll under the reader.
  let spySel = 'summary';

  $effect(() => {
    const target = sel;
    if (!pane || target === spySel) return;
    pane.querySelector(`[data-sel="${target}"]`)?.scrollIntoView({ block: 'start' });
    spySel = target; // the scroll event re-derives it if the section can't reach the top
  });

  // Scrollspy: the current section is the last one whose top has crossed the
  // pane's top — i.e. the one whose sticky header is showing. Every section
  // can get there: the last one is padded to the pane's height (see CSS).
  let raf = 0;
  function onScroll(): void {
    cancelAnimationFrame(raf);
    raf = requestAnimationFrame(() => {
      if (!pane || router.route.name !== 'review') return;
      const top = pane.getBoundingClientRect().top;
      let cur = 'summary';
      for (const el of pane.querySelectorAll<HTMLElement>('[data-sel]')) {
        if (el.getBoundingClientRect().top - top <= 2) cur = el.dataset.sel!;
      }
      if (cur !== spySel) {
        spySel = cur;
        replace({ name: 'review', pr: router.route.pr, sel: cur === 'summary' ? null : cur });
      }
    });
  }
</script>

<div class="diffs">
  <aside class="rail"><Tree /></aside>
  <div class="diff-main">
    <!-- The tree rail is hidden on narrow screens; this bar is the navigator
         there, jumping between Summary + every resource. -->
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
    <div class="diff-pane" bind:this={pane} onscroll={onScroll}>
      <section class="diff-section" data-sel="summary"><Overview /></section>
      {#each resources as r (r.id)}
        <section class="diff-section" data-sel={r.id}><Diff resource={r} /></section>
      {/each}
    </div>
  </div>
</div>
