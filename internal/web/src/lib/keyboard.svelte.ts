// Keyboard-first navigation. Active only outside text inputs and without
// modifier keys, so it never fights the browser or the filter box.
import { router } from './router.svelte';
import { adjacentPR, adjacentResource, goList, openSel } from './store.svelte';

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
        adjacentResource(1); // step down through Summary + resources
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
        openSel(r.pr, 'summary'); // jump to the Summary panel
        break;
      default:
        return;
    }
    e.preventDefault();
  });
}
