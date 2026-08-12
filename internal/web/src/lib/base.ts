// Runtime base path injected by the Go server into index.html. Empty string
// means konflate is served at the root path.
declare global {
	interface Window {
		KONFLATE_BASE_PATH?: string;
	}
}
export const basePath: string = window.KONFLATE_BASE_PATH ?? '';
