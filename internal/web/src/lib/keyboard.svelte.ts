// Keyboard-first navigation. Active only outside text inputs and without
// modifier keys, so it never fights the browser or the filter box.
import { router } from './router.svelte';
import { store, adjacentPR, adjacentResource, goList, setTab } from './store.svelte';
import { toggleViewed } from './viewed.svelte';

function isTyping(e: KeyboardEvent): boolean {
  const el = e.target as HTMLElement | null;
  return !!el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.isContentEditable);
}

export function initKeyboard(): void {
  window.addEventListener('keydown', (e) => {
    if (isTyping(e) || e.metaKey || e.ctrlKey || e.altKey) return;
    const r = router.route;
    if (r.name !== 'review') return;

    switch (e.key) {
      case 'Escape':
      case 'u':
        goList();
        break;
      case 'j':
        adjacentResource(1); // also jumps into the Diffs tab
        break;
      case 'k':
        adjacentResource(-1);
        break;
      case '[':
        adjacentPR(-1);
        break;
      case ']':
        adjacentPR(1);
        break;
      case 'o':
        setTab(r.tab === 'overview' ? 'diffs' : 'overview');
        break;
      case 'v': {
        if (!r.resource) return;
        const pr = store.prs.find((p) => p.number === r.pr);
        if (pr) toggleViewed(r.pr, pr.headSha, r.resource);
        break;
      }
      default:
        return;
    }
    e.preventDefault();
  });
}
