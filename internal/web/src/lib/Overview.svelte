<script lang="ts">
  import { router } from './router.svelte';
  import { store, diffIndex, openSel } from './store.svelte';
  import Icon from './Icon.svelte';
  import Copy from './Copy.svelte';
  import { mdiChevronDown, mdiChevronRight } from './icons';
  import type { BlastRadiusEntry } from './types';

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

  // Blast-radius dependents are "Kind ns/name" and are always the parent's own
  // kind (Flux dependsOn is same-kind), so name the kind once in the count and
  // list the bare "ns/name" — no per-row "Kustomization"/"HelmRelease" repeat.
  function blastKind(parent: string): string {
    const sp = parent.indexOf(' ');
    return sp < 0 ? '' : parent.slice(0, sp);
  }
  function bareRef(ref: string): string {
    const sp = ref.indexOf(' ');
    return sp < 0 ? ref : ref.slice(sp + 1);
  }
  // The count is the directly-named dependents — what the list below shows —
  // not the transitive closure, so the number always matches the names beneath it.
  function dependentsLabel(br: BlastRadiusEntry): string {
    const n = br.direct.length;
    const kind = blastKind(br.parent);
    const noun = n === 1 ? 'dependent' : 'dependents';
    return kind ? `${n} ${kind} ${noun}` : `${n} ${noun}`;
  }
</script>

{#snippet warningBody(w: { level: string; resource: string; detail: string })}
  <span class="flag-title">{w.resource}</span>
  <span class="flag-detail">{w.detail}</span>
{/snippet}

<!-- d can briefly be null while a new diff loads (ensureDiff clears it); the
     parent unmounts this view in the same flush, but guard rather than assert. -->
{#if d}
<div class="overview">
  <!-- The impact summary (scope counts + change delta) now rides in the sticky
       summary header (see Diffs.svelte); this content is just the sections. -->
  <!-- Two columns on a wide pane (see .ov-grid / .ov-col): the things a reviewer
       must act on — render failures, then cautions — lead in the left column; the
       informational blast radius and image list sit together in the right one. -->
  <div class="ov-grid">
    {#if d.failures?.length || d.warnings?.length}
      <div class="ov-col">
        {#if d.failures?.length}
          <section class="ov-section fail-section">
            <h3>Render failures <span class="ov-count">{d.failures.length}</span></h3>
            {#each d.failures as f}
              <div class="flag">
                <span class="flag-title">{f.parent}</span>
                <span class="flag-detail">{f.message}</span>
              </div>
            {/each}
          </section>
        {/if}

        {#if d.warnings?.length}
          <section class="ov-section caution-section">
            <h3>Cautions <span class="ov-count">{d.warnings.length}</span></h3>
            {#each d.warnings as w}
              {@const target = warningTarget(w.resource)}
              <!-- Cautions whose resource rendered into the diff deep-link to it. -->
              {#if target}
                <button class="flag flag-link {w.level}" title="View the resource diff" onclick={() => openWarning(target)}>
                  {@render warningBody(w)}
                </button>
              {:else}
                <div class="flag {w.level}">{@render warningBody(w)}</div>
              {/if}
            {/each}
          </section>
        {/if}
      </div>
    {/if}

    {#if d.blastRadius?.length || d.images?.length}
      <div class="ov-col">
        <!-- Blast radius: for each changed/failed app, how many downstream apps
             declare a transitive spec.dependsOn on it — the reconciliation reach a
             raw file diff can't show. Direct dependents are named; the count is the
             full transitive closure. Absent when nothing changed depends-on anything. -->
        {#if d.blastRadius?.length}
          <section class="ov-section">
            <h3>Blast radius <span class="ov-count">{d.blastRadius.length}</span></h3>
            {#each d.blastRadius as br}
              <div class="blast">
                <span class="blast-parent">{br.parent}</span>
                <span class="blast-count">{dependentsLabel(br)}</span>
                {#if br.direct?.length}
                  <div class="blast-deps">{br.direct.map(bareRef).join(', ')}</div>
                {/if}
              </div>
            {/each}
          </section>
        {/if}

        {#if d.images?.length}
          <section class="ov-section">
            <!-- A long image list collapses behind its count (imageCollapseThreshold)
                 so it doesn't crowd the blast radius above; a short one renders open. -->
            {#if imagesCollapsible}
              <button class="ov-head" aria-expanded={imagesOpen} onclick={() => (imagesOpen = !imagesOpen)}>
                <Icon path={imagesOpen ? mdiChevronDown : mdiChevronRight} size={14} />
                Image changes
                <span class="ov-count">{d.images.length}</span>
              </button>
            {:else}
              <h3>Image changes <span class="ov-count">{d.images.length}</span></h3>
            {/if}
            {#if !imagesCollapsible || imagesOpen}
              <ul class="img-list">
                {#each d.images as img}
                  <li class="img-change">
                    <!-- Name, from → to and copy share one line; ∅ = no reference on
                         that side (image added/removed) and the tooltip spells it out. -->
                    <div class="img-top">
                      <span class="img-name">{img.name}</span>
                      <span class="img-delta">
                        <span class="img-ver from" title={img.from || 'not present before this change'}>{shortVer(img.from)}</span>
                        <span class="img-arrow">→</span>
                        <span class="img-ver to" title={img.to || 'not present after this change'}>{shortVer(img.to)}</span>
                      </span>
                      {#if img.to}<Copy text={imageRef(img.name, img.to)} label="Copy new image reference" />{/if}
                    </div>
                  </li>
                {/each}
              </ul>
            {/if}
          </section>
        {/if}
      </div>
    {/if}
  </div>
</div>
{/if}
