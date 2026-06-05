// Theme: auto (follow OS), light, or dark. The chosen theme drives a `light`/
// `dark` class on <html>, which both the design tokens and the scoped chroma
// stylesheets key off. The preference persists in localStorage.

export type ThemePref = 'auto' | 'light' | 'dark';

const KEY = 'konflate-theme';
const mq = window.matchMedia('(prefers-color-scheme: dark)');

function load(): ThemePref {
  const v = localStorage.getItem(KEY);
  return v === 'light' || v === 'dark' || v === 'auto' ? v : 'auto';
}

export const theme = $state({ pref: load() });

export function effective(): 'light' | 'dark' {
  if (theme.pref === 'auto') return mq.matches ? 'dark' : 'light';
  return theme.pref;
}

export function applyTheme(): void {
  const eff = effective();
  const root = document.documentElement;
  root.classList.toggle('dark', eff === 'dark');
  root.classList.toggle('light', eff === 'light');
}

export function cycleTheme(): void {
  const order: ThemePref[] = ['auto', 'light', 'dark'];
  theme.pref = order[(order.indexOf(theme.pref) + 1) % order.length];
  localStorage.setItem(KEY, theme.pref);
  applyTheme();
}

export function initTheme(): void {
  applyTheme();
  mq.addEventListener('change', applyTheme);
}
