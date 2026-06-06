// Keyboard-first navigation. Active only outside text inputs and without
// modifier keys, so it never fights the browser or the filter box.
import { router } from './router.svelte';
import { adjacentPR, adjacentResource, goList, openSel } from './store.svelte';

// Two overlays: the shortcuts help (toggled by '?' or the topbar button) and the
// command palette (Cmd/Ctrl+K). They are mutually exclusive, and opening either
// remembers the focused element so closing restores it (WCAG 2.4.3 focus order).
export const help = $state({ open: false });
export const palette = $state({ open: false });

let restoreFocus: HTMLElement | null = null;

// open shows one overlay, closing the other first and recording the element to
// restore focus to on close.
function open(show: () => void): void {
  const active = document.activeElement;
  restoreFocus = active instanceof HTMLElement ? active : null;
  help.open = false;
  palette.open = false;
  show();
}

// closeOverlays shuts both overlays and returns focus to whatever opened them.
function closeOverlays(): void {
  help.open = false;
  palette.open = false;
  restoreFocus?.focus();
  restoreFocus = null;
}

export function toggleHelp(): void {
  if (help.open) closeOverlays();
  else open(() => (help.open = true));
}

export function togglePalette(): void {
  if (palette.open) closeOverlays();
  else open(() => (palette.open = true));
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
      closeOverlays();
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
