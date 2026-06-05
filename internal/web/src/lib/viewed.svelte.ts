// "Viewed" review-progress state, persisted in localStorage and keyed by PR +
// head SHA, so marks reset automatically when a PR is pushed to. GitHub-style:
// tick off resources as you review them across a large change.

const KEY = 'konflate-viewed';

type ViewedMap = Record<string, string[]>; // `${pr}:${sha}` -> resource ids

function load(): ViewedMap {
  try {
    return JSON.parse(localStorage.getItem(KEY) ?? '{}') as ViewedMap;
  } catch {
    return {};
  }
}

const data = $state<{ map: ViewedMap }>({ map: load() });

const key = (pr: number, sha: string) => `${pr}:${sha}`;

export function isViewed(pr: number, sha: string, id: string): boolean {
  return data.map[key(pr, sha)]?.includes(id) ?? false;
}

export function viewedCount(pr: number, sha: string): number {
  return data.map[key(pr, sha)]?.length ?? 0;
}

export function toggleViewed(pr: number, sha: string, id: string): void {
  const k = key(pr, sha);
  const arr = data.map[k] ?? [];
  data.map[k] = arr.includes(id) ? arr.filter((x) => x !== id) : [...arr, id];
  localStorage.setItem(KEY, JSON.stringify(data.map));
}
