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

  // Lazy-mount: every resource's <section> wrapper and its sticky header stay
  // in the DOM (so the tree, scrollspy, deep-links and j/k keep working), but
  // the heavy diff <table> inside only mounts once its section nears the
  // viewport. Mount-once — a section never unmounts — so expand state and the
  // scroll position never churn. On a large PR this turns "build every table up
  // front" into "build the few you're actually looking at": the render win.
  let mounted = $state<Record<string, boolean>>({});

  // The selection the scrollspy last derived. Navigation (tree click, j/k,
  // deep link) only scrolls when the route disagrees with it, so scroll-driven
  // route updates never re-scroll under the reader. Diffs remounts per PR (the
  // loading state unmounts it), so this never carries across PRs.
  let spySel = 'summary';

  $effect(() => {
    const target = sel;
    const present = resources; // re-run when the rendered resource set changes
    if (!pane || target === spySel) return;
    // A jumped-to section (deep link, tree click, j/k) mounts immediately so its
    // real table is in place as we scroll to it — no skeleton at the destination,
    // and the scroll lands on real content rather than a placeholder estimate.
    if (target !== 'summary') mounted[target] = true;
    // CSS.escape: `sel` comes from the URL hash, and an unescaped quote/bracket
    // would make querySelector throw inside the effect.
    const section = pane.querySelector(`[data-sel="${CSS.escape(target)}"]`);
    if (section) {
      section.scrollIntoView({ block: 'start' });
      spySel = target; // the scroll event re-derives it if the section can't reach the top
      return;
    }
    // No section for the target. Only a genuinely-missing resource — the diff
    // finished loading and doesn't contain it (a stale deep link) — bounces to
    // the Summary; if the sections just haven't rendered yet (a future change
    // that mounts during loading), wait for the next run rather than redirect.
    const stale = target !== 'summary' && !store.loading && !present.some((r) => r.id === target);
    if (stale && router.route.name === 'review') {
      spySel = 'summary';
      replace({ name: 'review', pr: router.route.pr, sel: null });
    }
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

  // One observer for every resource section, rooted on the scroll pane. The
  // generous margin mounts a section a couple of viewports before it scrolls in,
  // so the real table is ready and there's no skeleton flash in normal reading.
  // Sections register through the `lazy` action; any that mount before this
  // effect builds the observer wait in `pending`.
  let io: IntersectionObserver | null = null;
  const pending = new Set<HTMLElement>();

  $effect(() => {
    if (!pane) return;
    if (typeof IntersectionObserver === 'undefined') {
      // No observer (an ancient browser, or a non-DOM env): mount everything,
      // i.e. fall back to the previous always-rendered behavior.
      for (const r of resources) mounted[r.id] = true;
      return;
    }
    const obs = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (!e.isIntersecting) continue;
          const id = (e.target as HTMLElement).dataset.sel;
          if (id) mounted[id] = true;
          obs.unobserve(e.target); // mount-once: never tear a mounted table back down
        }
      },
      { root: pane, rootMargin: '200% 0px' },
    );
    io = obs;
    for (const el of pending) obs.observe(el);
    pending.clear();
    return () => {
      obs.disconnect();
      if (io === obs) io = null;
    };
  });

  // Action on each resource <section>: observe it (or queue it until the
  // observer exists), and stop observing if the section is removed.
  function lazy(node: HTMLElement) {
    if (io) io.observe(node);
    else pending.add(node);
    return {
      destroy() {
        io?.unobserve(node);
        pending.delete(node);
      },
    };
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
      <section class="diff-section" data-sel="summary">
        <!-- Same sticky container as Diff.svelte's res-header (keep the two in
             step), with a neutral status chip and a quiet title — the Summary
             is a section like the others, not a resource. -->
        <div class="res-header">
          <span class="res-status">summary</span>
          <span class="res-title res-title-quiet">Overview</span>
        </div>
        <Overview />
      </section>
      {#each resources as r (r.id)}
        <section class="diff-section" data-sel={r.id} use:lazy>
          <Diff resource={r} active={mounted[r.id] ?? false} />
        </section>
      {/each}
    </div>
  </div>
</div>
