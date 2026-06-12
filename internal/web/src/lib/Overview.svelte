<script lang="ts">
  import type { Snippet } from 'svelte';
  import { router } from './router.svelte';
  import { store, diffIndex, openSel } from './store.svelte';
  import Copy from './Copy.svelte';
  import type { BlastRadiusEntry } from './types';

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

<!-- One always-rendered column: the heading (with a count when non-empty and a
     severity colour when there's something to act on), then either the section's
     body or a green "No …" placeholder so the four columns line up even when a
     section is empty. The empty text follows the heading, so renaming a column
     renames its placeholder too. -->
{#snippet column(title: string, count: number, severity: string, body: Snippet)}
  <div class="ov-col">
    <section class="ov-section {count ? severity : ''}">
      <h3>{title}{#if count} <span class="ov-count">{count}</span>{/if}</h3>
      {#if count}
        {@render body()}
      {:else}
        <div class="ov-empty">No {title.toLowerCase()}</div>
      {/if}
    </section>
  </div>
{/snippet}

{#snippet warningBody(w: { level: string; resource: string; detail: string })}
  <span class="flag-title">{w.resource}</span>
  <span class="flag-detail">{w.detail}</span>
{/snippet}

{#snippet failuresBody()}
  {#each d?.failures ?? [] as f}
    <div class="flag">
      <span class="flag-title">{f.parent}</span>
      <span class="flag-detail">{f.message}</span>
    </div>
  {/each}
{/snippet}

{#snippet cautionsBody()}
  {#each d?.warnings ?? [] as w}
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
{/snippet}

{#snippet blastBody()}
  <!-- Blast radius: for each changed/failed app, how many downstream apps declare
       a transitive spec.dependsOn on it — the reconciliation reach a raw file diff
       can't show. Direct dependents are named; the count is the direct dependents. -->
  {#each d?.blastRadius ?? [] as br}
    <div class="blast">
      <span class="blast-parent">{br.parent}</span>
      <span class="blast-count">{dependentsLabel(br)}</span>
      {#if br.direct?.length}
        <div class="blast-deps">{br.direct.map(bareRef).join(', ')}</div>
      {/if}
    </div>
  {/each}
{/snippet}

{#snippet imagesBody()}
  <ul class="img-list">
    {#each d?.images ?? [] as img}
      <li class="img-change">
        <!-- Name, from → to and copy share one line; ∅ = no reference on that
             side (image added/removed) and the tooltip spells it out. -->
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
{/snippet}

<!-- d can briefly be null while a new diff loads (ensureDiff clears it); the
     parent unmounts this view in the same flush, but guard rather than assert. -->
{#if d}
<div class="overview">
  <!-- The impact summary (scope counts + change delta) now rides in the sticky
       summary header (see Diffs.svelte); this content is just the sections. -->
  <!-- Four columns, always shown so the layout is stable across PRs: the things a
       reviewer must act on lead — render failures (red), then cautions (amber) —
       followed by the informational blast radius and image changes. An empty
       section keeps its column and shows a green "No …" placeholder. The grid
       collapses to fewer columns as the pane narrows. -->
  <div class="ov-grid">
    {@render column('Render failures', d.failures?.length ?? 0, 'fail-section', failuresBody)}
    {@render column('Cautions', d.warnings?.length ?? 0, 'caution-section', cautionsBody)}
    {@render column('Blast radius', d.blastRadius?.length ?? 0, '', blastBody)}
    {@render column('Image changes', d.images?.length ?? 0, '', imagesBody)}
  </div>
</div>
{/if}
