/// <reference types="vite/client" />
/// <reference types="svelte" />

// Fontsource packages are side-effect CSS imports (they inject @font-face) and
// ship no type declarations; tell TS the modules exist.
declare module '@fontsource-variable/*';
