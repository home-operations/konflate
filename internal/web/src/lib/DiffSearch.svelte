<script lang="ts">
  // The floating find-in-diff bar (opened with '/' on the review screen — see
  // keyboard.svelte.ts). Searches the diff data rather than the DOM, so
  // lazy-mounted sections that defeat the browser's Ctrl+F are still found;
  // Enter / Shift+Enter (or the arrows) step through hits and jump.
  import Icon from './Icon.svelte';
  import { search, setQuery, step, closeSearch } from './search.svelte';
  import { mdiChevronDown, mdiChevronUp, mdiClose, mdiMagnify } from './icons';

  function onKeydown(e: KeyboardEvent): void {
    switch (e.key) {
      case 'Enter':
        step(e.shiftKey ? -1 : 1);
        break;
      case 'Escape':
        closeSearch();
        break;
      default:
        return;
    }
    e.preventDefault();
    e.stopPropagation();
  }

  // Autofocus the input when the bar mounts ({#if search.open} in Diffs).
  function focusOnMount(node: HTMLInputElement) {
    node.focus();
  }

  const counter = $derived(
    search.hits.length === 0
      ? search.q.trim().length >= 2
        ? 'no hits'
        : ''
      : search.cur >= 0
        ? `${search.cur + 1}/${search.hits.length}`
        : `${search.hits.length} ${search.hits.length === 1 ? 'hit' : 'hits'}`,
  );
</script>

<div class="diff-search" role="search" aria-label="Find in diff">
  <Icon path={mdiMagnify} size={14} />
  <input
    type="text"
    placeholder="Find in diff…"
    value={search.q}
    oninput={(e) => setQuery(e.currentTarget.value)}
    onkeydown={onKeydown}
    use:focusOnMount
    aria-label="Find in diff"
  />
  <span class="search-count" aria-live="polite">{counter}</span>
  <button
    class="btn btn-icon"
    onclick={() => step(-1)}
    disabled={search.hits.length === 0}
    title="Previous hit (Shift+Enter)"
    aria-label="Previous hit"
  >
    <Icon path={mdiChevronUp} size={14} />
  </button>
  <button
    class="btn btn-icon"
    onclick={() => step(1)}
    disabled={search.hits.length === 0}
    title="Next hit (Enter)"
    aria-label="Next hit"
  >
    <Icon path={mdiChevronDown} size={14} />
  </button>
  <button class="btn btn-icon" onclick={closeSearch} title="Close (Esc)" aria-label="Close search">
    <Icon path={mdiClose} size={14} />
  </button>
</div>
