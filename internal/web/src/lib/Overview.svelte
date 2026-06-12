<script lang="ts">
  import { router } from './router.svelte';
  import { store, diffIndex, openSel } from './store.svelte';
  import Icon from './Icon.svelte';
  import Copy from './Copy.svelte';
  import {
    mdiAlert,
    mdiPackageVariantClosed,
    mdiAlertCircleOutline,
    mdiSitemapOutline,
    mdiChevronDown,
    mdiChevronRight,
  } from './icons';

  const d = $derived(store.diff);

  // Image changes can run long on a big bump; collapse the list past this many so
  // it doesn't push the higher-signal cautions/failures down the pane. The count
  // stays in the header and one click expands it; shorter lists render open.
  // Diffs remounts per PR, so imagesOpen resets with each diff.
  const imageCollapseThreshold = 6;
  let imagesOpen = $state(false);
  const imagesCollapsible = $derived((d?.images?.length ?? 0) > imageCollapseThreshold);

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
</script>

{#snippet warningBody(w: { level: string; resource: string; detail: string })}
  <span class="warning-badge">
    <Icon path={mdiAlert} size={12} />
    {w.level}
  </span>
  <span class="warning-res">{w.resource}</span>
  <span class="warning-detail">{w.detail}</span>
{/snippet}

<!-- d can briefly be null while a new diff loads (ensureDiff clears it); the
     parent unmounts this view in the same flush, but guard rather than assert. -->
{#if d}
<div class="overview">
  <!-- The impact summary (scope counts + change delta) now rides in the sticky
       summary header (see Diffs.svelte); this content is just the sections. -->
  <!-- Two columns on a wide pane (see .ov-grid). Flags first — render failures,
       then cautions — then the informational blast radius and image list, so the
       things a reviewer must act on lead and don't get buried. -->
  <div class="ov-grid">
    {#if d.failures?.length}
      <section class="ov-section">
        <h3>
          <Icon path={mdiAlertCircleOutline} size={15} /> Render failures
          <span class="ov-count">{d.failures.length}</span>
        </h3>
        {#each d.failures as f}
          <div class="failure">
            <span class="failure-parent">{f.parent}</span>
            <div class="failure-msg">{f.message}</div>
          </div>
        {/each}
      </section>
    {/if}

    {#if d.warnings?.length}
      <section class="ov-section">
        <h3>Cautions <span class="ov-count">{d.warnings.length}</span></h3>
        {#each d.warnings as w}
          {@const target = warningTarget(w.resource)}
          <!-- Cautions whose resource rendered into the diff deep-link to it. -->
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

    <!-- Blast radius: for each changed/failed app, how many downstream apps
         declare a transitive spec.dependsOn on it — the reconciliation reach a
         raw file diff can't show. Direct dependents are named; the count is the
         full transitive closure. Absent when nothing changed depends-on anything. -->
    {#if d.blastRadius?.length}
      <section class="ov-section">
        <h3>
          <Icon path={mdiSitemapOutline} size={15} /> Blast radius
          <span class="ov-count">{d.blastRadius.length}</span>
        </h3>
        {#each d.blastRadius as br}
          <div class="blast">
            <span class="blast-parent">{br.parent}</span>
            <span class="blast-count">{br.transitive} {br.transitive === 1 ? 'dependent' : 'dependents'}</span>
            {#if br.direct?.length}
              <div class="blast-deps">{br.direct.join(', ')}</div>
            {/if}
          </div>
        {/each}
      </section>
    {/if}

    {#if d.images?.length}
      <section class="ov-section">
        <!-- A long image list collapses behind its count (imageCollapseThreshold)
             so it doesn't crowd out the flags above; a short one renders open. -->
        {#if imagesCollapsible}
          <button class="ov-head" aria-expanded={imagesOpen} onclick={() => (imagesOpen = !imagesOpen)}>
            <Icon path={imagesOpen ? mdiChevronDown : mdiChevronRight} size={14} />
            <Icon path={mdiPackageVariantClosed} size={15} /> Image changes
            <span class="ov-count">{d.images.length}</span>
          </button>
        {:else}
          <h3>
            <Icon path={mdiPackageVariantClosed} size={15} /> Image changes
            <span class="ov-count">{d.images.length}</span>
          </h3>
        {/if}
        {#if !imagesCollapsible || imagesOpen}
          <ul class="img-list">
            {#each d.images as img}
              <li class="img-change">
                <div class="img-head">
                  <span class="img-name">{img.name}</span>
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
        {/if}
      </section>
    {/if}
  </div>
</div>
{/if}
