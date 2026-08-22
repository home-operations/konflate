// Base path konflate is served under (empty string at the root). The app is
// hash-routed, so the document URL is always <basePath>/ or
// <basePath>/index.html and the base can be derived from it — the same way
// sw.js derives its own from self.location. No server cooperation needed.
export const basePath: string = window.location.pathname.replace(/\/(index\.html)?$/, '');
