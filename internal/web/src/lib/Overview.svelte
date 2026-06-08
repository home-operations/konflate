<script lang="ts">
  import { router } from './router.svelte';
  import { store, diffIndex, openSel } from './store.svelte';
  import Icon from './Icon.svelte';
  import Copy from './Copy.svelte';
  import { mdiAlertOctagon, mdiAlert, mdiPackageVariantClosed, mdiAlertCircleOutline, mdiOpenInNew } from './icons';

  const d = $derived(store.diff);

  // A warning's resource ("Kind ns/name") matches the diff resource's title, so
  // a warning can deep-link to the diff it flags. Null when the resource didn't
  // render into the diff (e.g. it only changed indirectly).
  function warningTarget(resource: string): string | null {
    return diffIndex().idByTitle.get(resource) ?? null;
  }
  function openWarning(id: string): void {
    if (router.route.name === 'review') openSel(router.route.pr, id);
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

  // Best-effort web link for an image repository, so a reviewer can jump from a
  // bump to the image's registry page (its tags, README, and from there the
  // upstream source / release notes). Returns null for registries with no
  // derivable web UI — the name then renders as plain text.
  function registryUrl(name: string): string | null {
    if (!name) return null;
    // Split an optional registry host from the repo path; a bare "org/repo" (no
    // host) is the implicit Docker Hub registry.
    const slash = name.indexOf('/');
    const head = slash === -1 ? '' : name.slice(0, slash);
    const hasHost = head.includes('.') || head.includes(':') || head === 'localhost';
    const host = hasHost ? head : 'docker.io';
    let repo = hasHost ? name.slice(slash + 1) : name;
    // A digest-pinned image carries its human tag on the name (e.g. x/y:4.5.0),
    // and a name may carry an @sha256 digest — drop either from the final
    // segment (the host's :port was already split off above).
    const lastSlash = repo.lastIndexOf('/');
    const tail = repo.slice(lastSlash + 1);
    const cut = tail.search(/[:@]/);
    if (cut !== -1) repo = repo.slice(0, lastSlash + 1) + tail.slice(0, cut);
    if (!repo) return null;
    const segs = repo.split('/');
    switch (host) {
      case 'ghcr.io':
        // ghcr images are overwhelmingly built from the like-named GitHub repo;
        // link there. Best-effort — a repackaged image may not have one.
        return segs.length >= 2 ? `https://github.com/${segs[0]}/${segs[1]}` : null;
      case 'quay.io':
        return `https://quay.io/repository/${repo}`;
      case 'docker.io':
      case 'index.docker.io':
      case 'registry-1.docker.io':
        if (segs.length === 1) return `https://hub.docker.com/_/${segs[0]}`;
        if (segs[0] === 'library') return `https://hub.docker.com/_/${segs.slice(1).join('/')}`;
        return `https://hub.docker.com/r/${repo}`;
      default:
        return null;
    }
  }
</script>

{#snippet warningBody(w: { level: string; resource: string; detail: string })}
  <span class="warning-badge">
    <Icon path={w.level === 'danger' ? mdiAlertOctagon : mdiAlert} size={12} />
    {w.level}
  </span>
  <span class="warning-res">{w.resource}</span>
  <span class="warning-detail">{w.detail}</span>
{/snippet}

<!-- d can briefly be null while a new diff loads (ensureDiff clears it); the
     parent unmounts this view in the same flush, but guard rather than assert. -->
{#if d}
<div class="overview">
  <div class="impact">
    <span class="impact-pill"><strong>{d.impact.resources}</strong> resources</span>
    <span class="impact-pill"><strong>{d.impact.parents}</strong> parents</span>
    <span class="impact-pill"><strong>{d.impact.crds}</strong> CRDs</span>
    {#if d.impact.namespaces?.length}
      <span class="impact-pill"><strong>{d.impact.namespaces.length}</strong> namespaces</span>
    {/if}
    <!-- Zero counts stay neutral; a tinted "+0" is noise. -->
    <span class="impact-pill" class:add={d.summary.added > 0}>+{d.summary.added} added</span>
    <span class="impact-pill" class:chg={d.summary.changed > 0}>{d.summary.changed} changed</span>
    <span class="impact-pill" class:del={d.summary.removed > 0}>−{d.summary.removed} removed</span>
    <!-- The diff was capped: counts above are the true totals, but this many
         resources were not rendered. The review here is partial. -->
    {#if d.truncated}
      <span class="impact-pill trunc" title="This diff exceeded the render cap; {d.truncated} resource diffs were not rendered."
        >{d.truncated} not shown</span
      >
    {/if}
  </div>

  {#if d.warnings?.length}
    <section class="ov-section">
      <h3>Warnings</h3>
      {#each d.warnings as w}
        {@const target = warningTarget(w.resource)}
        <!-- Warnings whose resource rendered into the diff deep-link to it. -->
        {#if target}
          <button class="warning warning-link {w.level}" title="View the resource diff" onclick={() => openWarning(target)}>
            {@render warningBody(w)}
          </button>
        {:else}
          <div class="warning {w.level}">{@render warningBody(w)}</div>
        {/if}
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
              {#if registryUrl(img.name)}
                <a class="img-name img-name-link" href={registryUrl(img.name)} target="_blank" rel="noopener noreferrer" title={`Open ${img.name} on its registry`}>
                  {img.name}<Icon path={mdiOpenInNew} size={11} />
                </a>
              {:else}
                <span class="img-name">{img.name}</span>
              {/if}
              {#if img.to}<Copy text={imageRef(img.name, img.to)} label="Copy new image reference" />{/if}
            </div>
            <!-- ∅ = no reference on that side (image added/removed); tooltip spells it out. -->
            <div class="img-delta">
              <span class="img-ver from" title={img.from || 'not present before this change'}>{shortVer(img.from)}</span>
              <span class="img-arrow">→</span>
              <span class="img-ver to" title={img.to || 'not present after this change'}>{shortVer(img.to)}</span>
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
</div>
{/if}
