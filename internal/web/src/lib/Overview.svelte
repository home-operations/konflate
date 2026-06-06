<script lang="ts">
  import { router } from './router.svelte';
  import { store, openDiffs } from './store.svelte';
  import Icon from './Icon.svelte';
  import Copy from './Copy.svelte';
  import { mdiAlertOctagon, mdiAlert, mdiPackageVariantClosed, mdiAlertCircleOutline } from './icons';

  const d = $derived(store.diff!);

  function open(id: string) {
    if (router.route.name === 'review') openDiffs(router.route.pr, id);
  }

  // Shorten an "algo:hexdigest" (e.g. sha256:<64 hex>) to "algo:<12 hex>…" so a
  // digest-pinned image doesn't blow out the layout; tags are short already and
  // shown in full. The full value stays in the title tooltip and the copy button.
  function shortVer(v: string): string {
    if (!v) return '∅';
    const i = v.indexOf(':');
    if (i < 0) return v; // a tag — no algo prefix
    const hex = v.slice(i + 1);
    return /^[0-9a-f]+$/i.test(hex) && hex.length > 12 ? `${v.slice(0, i + 1)}${hex.slice(0, 12)}…` : v;
  }

  // Reconstruct a pullable reference for the copy button: a digest joins the name
  // with '@' (a tag can never contain ':', so a ':' in the version means digest).
  function imageRef(name: string, ver: string): string {
    return ver.includes(':') ? `${name}@${ver}` : `${name}:${ver}`;
  }
</script>

<div class="overview">
  <div class="impact">
    <span class="impact-pill"><strong>{d.impact.resources}</strong> resources</span>
    <span class="impact-pill"><strong>{d.impact.parents}</strong> parents</span>
    <span class="impact-pill"><strong>{d.impact.crds}</strong> CRDs</span>
    {#if d.impact.namespaces?.length}
      <span class="impact-pill"><strong>{d.impact.namespaces.length}</strong> namespaces</span>
    {/if}
    <span class="impact-pill add">+{d.summary.added} added</span>
    <span class="impact-pill chg">{d.summary.changed} changed</span>
    <span class="impact-pill del">−{d.summary.removed} removed</span>
  </div>

  {#if d.warnings?.length}
    <section class="ov-section">
      <h3>Warnings</h3>
      {#each d.warnings as w}
        <div class="warning {w.level}">
          <span class="warning-badge">
            <Icon path={w.level === 'danger' ? mdiAlertOctagon : mdiAlert} size={12} />
            {w.level}
          </span>
          <span class="warning-res">{w.resource}</span>
          <div class="warning-detail">{w.detail}</div>
        </div>
      {/each}
    </section>
  {/if}

  {#if d.images?.length}
    <section class="ov-section">
      <h3><Icon path={mdiPackageVariantClosed} size={15} /> Image changes</h3>
      <ul class="img-list">
        {#each d.images as img}
          <li class="img-change">
            <div class="img-head">
              <span class="img-name">{img.name}</span>
              {#if img.to}<Copy text={imageRef(img.name, img.to)} label="Copy new image reference" />{/if}
            </div>
            <div class="img-delta">
              <span class="img-ver from" title={img.from || undefined}>{shortVer(img.from)}</span>
              <span class="img-arrow">→</span>
              <span class="img-ver to" title={img.to || undefined}>{shortVer(img.to)}</span>
            </div>
            {#if img.refs?.length}
              <div class="img-refs">{img.refs.join(', ')}</div>
            {/if}
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  {#if d.failures?.length}
    <section class="ov-section">
      <h3><Icon path={mdiAlertCircleOutline} size={15} /> Render failures</h3>
      {#each d.failures as f}
        <div class="failure">
          <span class="failure-parent">{f.parent}</span>
          <div class="failure-msg">{f.message}</div>
        </div>
      {/each}
    </section>
  {/if}

  <section class="ov-section">
    <h3>Changed resources</h3>
    {#each d.tree ?? [] as parent}
      <div class="ov-parent">{parent.label}</div>
      {#each parent.kinds as kind}
        {#each kind.items as item}
          <button class="ov-res" onclick={() => open(item.id)}>
            <span class="ov-dot status-{item.status}" aria-hidden="true"></span>
            <span class="ov-kind">{kind.kind}</span>
            <span class="ov-name status-{item.status}">{item.name}</span>
            <span class="spacer"></span>
            {#if item.add}<span class="add">+{item.add}</span>{/if}
            {#if item.del}<span class="del">-{item.del}</span>{/if}
          </button>
        {/each}
      {/each}
    {/each}
  </section>
</div>
