// Mirrors internal/api (pr.go, response.go, diff.go). The Go package doc notes
// these types are the contract.

export type JobStatus = 'pending' | 'running' | 'ready' | 'error';

export interface Label {
  name: string;
  color?: string; // hex without '#'; absent when the forge gives no color
}

export interface PR {
  number: number;
  title: string;
  author: string;
  authorAvatar?: string; // same-origin /api/avatar proxy path, or absent
  createdAt?: string; // when the PR was opened on the forge
  state: string;
  open: boolean; // normalized open flag (forge state strings differ)
  merged?: boolean; // closed via merge (vs abandoned)
  draft: boolean;
  headRef: string;
  headSha: string;
  baseRef: string;
  labels: Label[] | null;
  url: string;
}

export interface Signals {
  resources: number;
  danger: number;
  caution: number;
  images: number;
  failures: number;
}

export interface PRStatus extends PR {
  status: JobStatus;
  error?: string;
  refreshError?: string; // last re-render failed, but a prior diff is still shown
  updatedAt: string;
  closedAt?: string; // set once merged; UI groups these below open PRs
  signals?: Signals;
  mergeCommand?: string; // "copy to merge" CLI command; set only for open PRs when enabled
}

export interface DiffSummary {
  changed: number;
  added: number;
  removed: number;
}

export interface Impact {
  resources: number;
  parents: number;
  namespaces: string[] | null;
  crds: number;
}

export interface ImageChange {
  name: string;
  from: string;
  to: string;
  refs: string[] | null;
}

export interface RenderFailure {
  parent: string;
  message: string;
}

export interface Warning {
  level: 'danger' | 'caution';
  rule: string;
  resource: string;
  detail: string;
}

export interface DiffTreeItem {
  id: string;
  name: string;
  status: string;
  add: number;
  del: number;
}

export interface DiffTreeKind {
  kind: string;
  items: DiffTreeItem[];
}

export interface DiffTreeParent {
  label: string;
  kinds: DiffTreeKind[];
}

export interface UnifiedRow {
  hunk?: boolean;
  folded?: boolean;
  fold?: string;
  count?: number;
  kind?: 'ctx' | 'add' | 'del';
  oldNo?: number;
  newNo?: number;
  html?: string;
}

export interface SideCell {
  kind: 'ctx' | 'add' | 'del' | 'blank';
  no?: number;
  html?: string;
}

export interface SideRow {
  hunk?: boolean;
  folded?: boolean;
  fold?: string;
  count?: number;
  left: SideCell;
  right: SideCell;
}

export interface DiffResource {
  id: string;
  title: string;
  kind: string;
  name: string;
  parent: string;
  status: string;
  add: number;
  del: number;
  unified: UnifiedRow[];
  side: SideRow[];
}

export interface DiffResult {
  prNumber: number;
  headSha: string;
  summary: DiffSummary;
  impact: Impact;
  images: ImageChange[] | null;
  failures: RenderFailure[] | null;
  warnings: Warning[] | null;
  chromaCss: string;
  tree: DiffTreeParent[] | null;
  resources: DiffResource[] | null;
}

export interface DiffEnvelope {
  status: JobStatus;
  pr: PR;
  diff?: DiffResult;
  error?: string;
  refreshError?: string; // last re-render failed; diff is the last-good render
  mergeCommand?: string; // "copy to merge" CLI command; set only for open PRs when enabled
}

// Discriminated on `type`: a "status" event always carries a status (and maybe
// an error); a "removed" event is just the PR number. This lets the handler
// narrow on `ev.type` instead of asserting `status` is present.
export type WSEvent =
  | { type: 'status'; number: number; status: JobStatus; error?: string }
  | { type: 'removed'; number: number };

export interface Meta {
  forge: string;
  repo: string;
  refreshIntervalSeconds: number;
}
