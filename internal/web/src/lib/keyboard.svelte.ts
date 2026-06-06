// Keyboard-first navigation. Active only outside text inputs and without
// modifier keys, so it never fights the browser or the filter box.
import { router } from './router.svelte';
import { adjacentPR, adjacentResource, goList, openSel } from './store.svelte';

// The shortcuts help overlay — toggled by '?' (and the topbar button), closed
// by Escape or a backdrop click.
export const help = $state({ open: false });
export function toggleHelp(): void {
  help.open = !help.open;
}

// The command palette — Cmd/Ctrl+K from anywhere (even inside an input).
// It owns its other keys (arrows, Enter, Escape) locally.
export const palette = $state({ open: false });
export function togglePalette(): void {
  palette.open = !palette.open;
}

function isTyping(e: KeyboardEvent): boolean {
  const el = e.target as HTMLElement | null;
  return !!el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.isContentEditable);
}

export function initKeyboard(): void {
  window.addEventListener('keydown', (e) => {
    // Cmd/Ctrl+K first: it must work everywhere, including inside the filter
    // box (where everything below is suppressed).
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
      togglePalette();
      e.preventDefault();
      return;
    }

    if (isTyping(e) || e.metaKey || e.ctrlKey || e.altKey) return;

    // '?' toggles the help on any screen; while open, Escape closes it
    // (instead of leaving the review underneath).
    if (e.key === '?') {
      toggleHelp();
      e.preventDefault();
      return;
    }
    if (help.open && e.key === 'Escape') {
      help.open = false;
      e.preventDefault();
      return;
    }

    const r = router.route;

    // On the list, '/' jumps to the filter box (the only input there).
    if (r.name === 'list') {
      if (e.key === '/') {
        document.querySelector<HTMLInputElement>('.pr-search')?.focus();
        e.preventDefault();
      }
      return;
    }

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
