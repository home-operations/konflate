<script lang="ts">
  import { router, replace } from './router.svelte';
  import { store, adjacentResource, selectables } from './store.svelte';
  import { search, closeSearch } from './search.svelte';
  import DiffSearch from './DiffSearch.svelte';
  import Tree from './Tree.svelte';
  import Diff from './Diff.svelte';
  import Overview from './Overview.svelte';
  import Icon from './Icon.svelte';
  import MergeCommand from './MergeCommand.svelte';
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

  // The element that actually scrolls. On desktop the diff pane scrolls inside
  // fixed chrome; on mobile (≤900px — where the rail is hidden and the switcher
  // takes over) the whole review column scrolls instead, so the PR header
  // scrolls away under the sticky switcher and the diff gets the full viewport
  // rather than ~half. closest() reaches the .review container that wraps both
  // the header and this pane (an ancestor, mounted before it). The scrollspy,
  // lazy-mount observer and scroll listener all key off this.
  const mobileMQ = '(max-width: 900px)';
  const layout = $state({
    mobile: typeof window !== 'undefined' && window.matchMedia(mobileMQ).matches,
  });
  $effect(() => {
    if (typeof window === 'undefined') return;
    const mq = window.matchMedia(mobileMQ);
    const sync = () => (layout.mobile = mq.matches);
    mq.addEventListener('change', sync);
    return () => mq.removeEventListener('change', sync);
  });
  const scroller = $derived(
    layout.mobile && pane ? ((pane.closest('.review') as HTMLElement | null) ?? pane) : pane,
  );

  // On mobile the switcher is sticky at the top of the scroll, so it overlaps
  // the leading edge. A jumped-to section must land below it (scroll-margin, via
  // the --stuck custom property) and the scrollspy's "crossed the top" line must
  // sit below it too — both by the switcher's measured height. 0 on desktop
  // (the switcher is hidden), so that path is unchanged.
  let switcherEl = $state<HTMLElement | null>(null);
  let stuckPx = $state(0);
  $effect(() => {
    stuckPx = layout.mobile && switcherEl ? switcherEl.offsetHeight : 0;
  });

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

  // After a programmatic jump (tree click, j/k, deep link) the scroll — and any
  // lazy-mount / content-visibility reflow it triggers — takes a moment to
  // settle. Suppress the scrollspy briefly so a mid-settle reading can't fight
  // the jump and bounce the selection around. A genuine user scroll after the
  // timer clears re-derives normally.
  let spyLocked = false;
  let spyLockTimer: ReturnType<typeof setTimeout> | undefined;
  function lockSpy(): void {
    spyLocked = true;
    clearTimeout(spyLockTimer);
    spyLockTimer = setTimeout(() => (spyLocked = false), 250);
  }

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
      spySel = target;
      lockSpy(); // hold the spy off until the scroll + any reflow settle
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
  // scroll container's top — i.e. the one the switcher labels. Runs against
  // `scroller` so it tracks whichever element actually scrolls (the pane on
  // desktop, the review column on mobile).
  let raf = 0;
  function onScroll(): void {
    cancelAnimationFrame(raf);
    raf = requestAnimationFrame(() => {
      const sc = scroller;
      if (spyLocked || !sc || router.route.name !== 'review') return;
      const top = sc.getBoundingClientRect().top + stuckPx;
      let cur = 'summary';
      for (const el of sc.querySelectorAll<HTMLElement>('[data-sel]')) {
        if (el.getBoundingClientRect().top - top <= 2) cur = el.dataset.sel!;
      }
      if (cur !== spySel) {
        spySel = cur;
        replace({ name: 'review', pr: router.route.pr, sel: cur === 'summary' ? null : cur });
      }
    });
  }

  // Bind the scrollspy to whatever actually scrolls (see `scroller`), and
  // re-bind if the breakpoint flips — an inline onscroll can't follow the
  // element across the desktop/mobile boundary.
  $effect(() => {
    const sc = scroller;
    if (!sc) return;
    sc.addEventListener('scroll', onScroll, { passive: true });
    return () => sc.removeEventListener('scroll', onScroll);
  });

  // Lazy-mount observer, rooted on the scroll container. The generous margin
  // mounts a section a couple of viewports before it scrolls in, so the real
  // table is ready and there's no skeleton flash in normal reading. The `io`
  // and `sections` registry rebuild if `scroller` flips (breakpoint change);
  // `sections` lets that rebuild re-observe everything still on screen.
  let io: IntersectionObserver | null = null;
  const sections = new Set<HTMLElement>();

  $effect(() => {
    const sc = scroller;
    if (!sc) return;
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
      { root: sc, rootMargin: '200% 0px' },
    );
    io = obs;
    for (const node of sections) obs.observe(node);
    return () => {
      obs.disconnect();
      if (io === obs) io = null;
    };
  });

  // Close the search when the review unmounts (back to the list, or the next
  // PR remounting Diffs) so a stale query never carries across.
  $effect(() => closeSearch);

  // Action on each resource <section>: register it (so an observer rebuild can
  // re-observe it) and observe it now if the observer already exists.
  function lazy(node: HTMLElement) {
    sections.add(node);
    io?.observe(node);
    return {
      destroy() {
        sections.delete(node);
        io?.unobserve(node);
      },
    };
  }
</script>

<div class="diffs">
  <aside class="rail"><Tree /></aside>
  <div class="diff-main">
    {#if search.open}
      <DiffSearch />
    {/if}
    <!-- The tree rail is hidden on narrow screens; this bar is the navigator
         there, jumping between Summary + every resource. On mobile it's the one
         bar pinned while the header + diff scroll past (see CSS / scroller). -->
    <div class="diff-switcher" bind:this={switcherEl}>
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
    <div class="diff-pane" bind:this={pane} style="--stuck: {stuckPx}px">
      <section class="diff-section" data-sel="summary">
        <!-- The rail's tree node already labels this section "Summary"; a second
             "Summary" here read as a duplicate, so the header carries no title of
             its own. It still mirrors Diff.svelte's res-header (sticky, same
             height) to keep the level-bar aligned with the rail, and hosts the
             copy-to-merge command on the right for open PRs with the feature on. -->
        <div class="res-header summary-header">
          {#if store.diffMergeCommand}
            <MergeCommand command={store.diffMergeCommand} />
          {/if}
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
