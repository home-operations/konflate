import type { DiffEnvelope, DiffResult, PRStatus } from '../src/lib/types';

// A compact dual-theme chroma stylesheet so the fixture diff is highlighted in
// the screenshots, mirroring the scoping the real renderer emits.
export const chromaCss = `
.light .chroma .nt{color:#116329}.light .chroma .p{color:#1f2328}.light .chroma .s,.light .chroma .l{color:#0a3069}.light .chroma .m{color:#0550ae}.light .chroma .kc{color:#cf222e}
.dark .chroma .nt{color:#7ee787}.dark .chroma .p{color:#e6edf3}.dark .chroma .s,.dark .chroma .l{color:#a5d6ff}.dark .chroma .m{color:#79c0ff}.dark .chroma .kc{color:#ff7b72}
`;

const k = (key: string) => `<span class="nt">${key}</span><span class="p">:</span> `;

export const samplePRs: PRStatus[] = [
  {
    number: 142,
    title: 'feat(rook-ceph): bump operator to v1.15.0',
    author: 'renovate[bot]',
    // Tiny inline PNG so the avatar renders in tests without a network round-trip
    // (real instances get a same-origin /api/avatar path here).
    authorAvatar:
      'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M8AAAMBAQDJ/pLvAAAAAElFTkSuQmCC',
    state: 'open',
    open: true,
    draft: false,
    headRef: 'renovate/rook-ceph',
    headSha: 'a1b2c3d4e5f6',
    baseRef: 'main',
    labels: ['area/storage'],
    url: 'https://github.com/acme/home-ops/pull/142',
    status: 'ready',
    updatedAt: '2026-06-04T12:00:00Z',
    signals: { resources: 3, danger: 1, caution: 1, images: 1, failures: 1 },
  },
  {
    number: 138,
    title: 'chore: scale down legacy web deployment',
    author: 'octocat',
    state: 'open',
    open: true,
    draft: true,
    headRef: 'chore/scale-web',
    headSha: 'f6e5d4c3',
    baseRef: 'staging',
    labels: [],
    url: 'https://github.com/acme/home-ops/pull/138',
    status: 'running',
    updatedAt: '2026-06-04T11:30:00Z',
  },
  {
    number: 131,
    title: 'fix(media): correct plex values schema',
    author: 'octocat',
    state: 'open',
    open: true,
    draft: false,
    headRef: 'fix/plex',
    headSha: 'deadbeef',
    baseRef: 'main',
    labels: ['area/media'],
    url: 'https://github.com/acme/home-ops/pull/131',
    status: 'error',
    error: 'render failed',
    updatedAt: '2026-06-04T10:00:00Z',
  },
  {
    number: 128,
    title: 'feat(monitoring): add loki',
    author: 'octocat',
    state: 'merged',
    open: false,
    merged: true,
    draft: false,
    headRef: 'feat/loki',
    headSha: 'cafe1234',
    baseRef: 'main',
    labels: ['area/observability'],
    url: 'https://github.com/acme/home-ops/pull/128',
    status: 'ready',
    updatedAt: '2026-06-02T09:00:00Z',
    closedAt: '2026-06-02T09:05:00Z',
    signals: { resources: 5, danger: 0, caution: 0, images: 2, failures: 0 },
  },
];

export const sampleDiff: DiffResult = {
  prNumber: 142,
  headSha: 'a1b2c3d4e5f6',
  summary: { added: 2, changed: 3, removed: 1 },
  impact: { resources: 6, parents: 2, namespaces: ['default', 'rook-ceph'], crds: 1 },
  images: [
    {
      name: 'ghcr.io/rook/ceph',
      from: 'v1.14.9',
      to: 'v1.15.0',
      refs: ['Deployment rook-ceph/rook-ceph-operator'],
    },
    {
      // Digest-pinned image: the full sha256 digests must be shortened in the UI.
      name: 'ghcr.io/thelounge/thelounge:4.5.0',
      from: 'sha256:9c3667236b1a82cf79b1b35e012ddf58e1e2de46f3596befbc699825c0793680',
      to: 'sha256:7f2fff6e264411ce8608bd1fdf5142a3cd980677b0479e7e3702aadf18cd1abc',
      refs: ['Deployment default/thelounge'],
    },
  ],
  failures: [
    { parent: 'HelmRelease media/plex', message: "values don't meet the schema: .image.tag is required" },
  ],
  warnings: [
    {
      level: 'danger',
      rule: 'removed-statefulset',
      resource: 'StatefulSet default/postgres',
      detail: 'removed StatefulSet — its PersistentVolumeClaims and data may be deleted',
    },
    {
      level: 'caution',
      rule: 'replicas-zero',
      resource: 'Deployment default/web',
      detail: 'replicas set to 0 — the workload will be scaled to zero',
    },
  ],
  chromaCss,
  tree: [
    {
      label: 'HelmRelease rook-ceph/rook-ceph',
      kinds: [
        { kind: 'Deployment', items: [{ id: 'r0', name: 'rook-ceph/rook-ceph-operator', status: 'changed', add: 1, del: 1 }] },
        { kind: 'ConfigMap', items: [{ id: 'r1', name: 'rook-ceph/rook-config', status: 'added', add: 4, del: 0 }] },
      ],
    },
    {
      label: 'Kustomization default/apps',
      kinds: [
        { kind: 'StatefulSet', items: [{ id: 'r2', name: 'default/postgres', status: 'removed', add: 0, del: 5 }] },
      ],
    },
  ],
  resources: [
    {
      id: 'r0',
      title: 'Deployment rook-ceph/rook-ceph-operator',
      kind: 'Deployment',
      name: 'rook-ceph/rook-ceph-operator',
      parent: 'HelmRelease rook-ceph/rook-ceph',
      status: 'changed',
      add: 1,
      del: 1,
      unified: [
        { kind: 'ctx', oldNo: 1, newNo: 1, html: `${k('apiVersion')}<span class="s">apps/v1</span>` },
        { kind: 'ctx', oldNo: 2, newNo: 2, html: `${k('kind')}<span class="s">Deployment</span>` },
        // Folded context between the header and the changed line — revealed on expand.
        { hunk: true, fold: 'g1', count: 2 },
        { folded: true, fold: 'g1', kind: 'ctx', oldNo: 3, newNo: 3, html: `${k('metadata')}` },
        { folded: true, fold: 'g1', kind: 'ctx', oldNo: 4, newNo: 4, html: `  ${k('name')}<span class="s">rook-ceph-operator</span>` },
        { kind: 'ctx', oldNo: 16, newNo: 16, html: `    ${k('containers')}` },
        // Word-level highlight: only "14.9" → "15.0" is wrapped in .wd.
        { kind: 'del', oldNo: 17, html: `      ${k('image')}<span class="s">ghcr.io/rook/ceph:v1.<span class="wd">14.9</span></span>` },
        { kind: 'add', newNo: 17, html: `      ${k('image')}<span class="s">ghcr.io/rook/ceph:v1.<span class="wd">15.0</span></span>` },
        { kind: 'ctx', oldNo: 18, newNo: 18, html: `      ${k('replicas')}<span class="m">1</span>` },
      ],
      side: [
        {
          left: { kind: 'del', no: 17, html: `      ${k('image')}<span class="s">ghcr.io/rook/ceph:v1.<span class="wd">14.9</span></span>` },
          right: { kind: 'add', no: 17, html: `      ${k('image')}<span class="s">ghcr.io/rook/ceph:v1.<span class="wd">15.0</span></span>` },
        },
      ],
    },
    {
      id: 'r1',
      title: 'ConfigMap rook-ceph/rook-config',
      kind: 'ConfigMap',
      name: 'rook-ceph/rook-config',
      parent: 'HelmRelease rook-ceph/rook-ceph',
      status: 'added',
      add: 4,
      del: 0,
      unified: [
        { kind: 'add', newNo: 1, html: `${k('apiVersion')}<span class="s">v1</span>` },
        { kind: 'add', newNo: 2, html: `${k('kind')}<span class="s">ConfigMap</span>` },
      ],
      side: [],
    },
    {
      id: 'r2',
      title: 'StatefulSet default/postgres',
      kind: 'StatefulSet',
      name: 'default/postgres',
      parent: 'Kustomization default/apps',
      status: 'removed',
      add: 0,
      del: 5,
      unified: [{ kind: 'del', oldNo: 1, html: `${k('kind')}<span class="s">StatefulSet</span>` }],
      side: [],
    },
  ],
};

export const diffEnvelope: DiffEnvelope = {
  status: 'ready',
  pr: samplePRs[0],
  diff: sampleDiff,
};
