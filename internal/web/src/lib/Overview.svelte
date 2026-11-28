<script lang="ts">
  import type { Snippet } from 'svelte';
  import { router } from './router.svelte';
  import { store, diffIndex, openSel, blockersFirst } from './store.svelte';
  import Copy from './Copy.svelte';
  import Icon from './Icon.svelte';
  import { mdiAlertOctagonOutline, mdiCheck, mdiCloseCircleOutline, mdiCircleOutline } from './icons';
  import type { ImageChange } from './types';

  const d = $derived(store.diff);

  // Blockers lead the column: they fail the check, so they're what the reviewer
  // must act on first.
  const warnings = $derived(blockersFirst(d?.warnings ?? []));
  const hasBlocker = $derived(warnings.some((w) => w.level === 'blocking'));

  // The registry verdict on an image's head-side ref (see ImageChange.upstream).
  // A removal has no head ref and gets no marker; an absent verdict is shown as
  // "unverified" rather than nothing, so a clean-looking column never implies
  // the images were confirmed.
  type Upstream = { cls: string; icon: string; text: string; hint: string };
  function upstream(img: ImageChange): Upstream | null {
    if (!img.to) return null;
    switch (img.upstream) {
      case 'found':
        return { cls: 'found', icon: mdiCheck, text: 'found', hint: 'Confirmed present in its registry' };
      case 'missing':
        return {
          cls: 'missing',
          icon: mdiCloseCircleOutline,
          text: 'not found',
          hint: 'Not found in upstream registry; this image would fail to pull',
        };
      default:
        return {
          cls: 'unverified',
          icon: mdiCircleOutline,
          text: 'unverified',
          hint: "Not checked against its registry: verification is off, this is a fork PR, or the registry didn't answer",
        };
    }
  }

  // A warning's resource ("Kind ns/name") matches the diff resource's title, so
  // a warning can deep-link to the diff it flags. Null when the resource didn't
  // render into the diff (e.g. it only changed indirectly).
  function warningTarget(resource: string): string | null {
    return diffIndex().idByTitle.get(resource) ?? null;
  }
  function openWarning(id: string): void {
    if (router.route.name === 'review') openSel(router.route.pr, id);
  }

  // Shorten an "algo:hexdigest" (e.g. sha256:<64 hex>) to "algo:<6 hex>…" so a
  // digest-pinned image doesn't blow out the layout; tags are short already and
  // shown in full. The full value stays in the title tooltip and the copy button.
  function shortVer(v: string): string {
    if (!v) return '∅';
    const i = v.indexOf(':');
    if (i < 0) return v; // a tag — no algo prefix
    const hex = v.slice(i + 1);
    return /^[0-9a-f]+$/i.test(hex) && hex.length > 6 ? `${v.slice(0, i + 1)}${hex.slice(0, 6)}…` : v;
  }

  // Reconstruct a pullable reference for the copy button. A tag can never
  // contain ':' and a bare digest ("sha256:…", or any other algorithm) always
  // does, so a version with ':' and no '@' is a bare digest and joins with '@';
  // a tag — including a digest-pinned one, "1.2.3@sha256:…" — joins with ':'.
  function imageRef(name: string, ver: string): string {
    return ver.includes(':') && !ver.includes('@') ? `${name}@${ver}` : `${name}:${ver}`;
  }
</script>

<!-- One always-rendered column: the heading (with a count when non-empty and a
     severity colour when there's something to act on), then either the section's
     body or a green "No …" placeholder so the three columns line up even when a
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
  <span class="flag-title"
    >{#if w.level === 'blocking'}<span class="badge blocking" title="Fails the check — the change would not deploy"
        ><Icon path={mdiAlertOctagonOutline} size={12} /> blocking</span
      > {/if}{w.resource}</span
  >
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
  {#each warnings as w}
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

{#snippet imagesBody()}
  <ul class="img-list">
    {#each d?.images ?? [] as img}
      {@const up = upstream(img)}
      <li class="img-change">
        <!-- Name, from → to, copy and the registry verdict share one line; ∅ = no
             reference on that side (image added/removed) and the tooltip spells it out. -->
        <div class="img-top">
          <span class="img-name">{img.name}</span>
          <span class="img-delta">
            <span class="img-ver from" title={img.from || 'not present before this change'}>{shortVer(img.from)}</span>
            <span class="img-arrow">→</span>
            <span class="img-ver to" title={img.to || 'not present after this change'}>{shortVer(img.to)}</span>
          </span>
          {#if img.to}<Copy text={imageRef(img.name, img.to)} label="Copy new image reference" />{/if}
          {#if up}
            <span class="img-upstream {up.cls}" title={up.hint}><Icon path={up.icon} size={12} /> {up.text}</span>
          {/if}
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
  <!-- Three columns, always shown so the layout is stable across PRs: the things a
       reviewer must act on lead — render failures (red), then cautions (amber) —
       followed by the informational image changes. An empty
       section keeps its column and shows a green "No …" placeholder. The grid
       collapses to fewer columns as the pane narrows. -->
  <div class="ov-grid">
    {@render column('Render failures', d.failures?.length ?? 0, 'fail-section', failuresBody)}
    <!-- A blocker turns the column red: it fails the check, like a render failure. -->
    {@render column('Cautions', warnings.length, hasBlocker ? 'fail-section' : 'caution-section', cautionsBody)}
    {@render column('Image changes', d.images?.length ?? 0, '', imagesBody)}
  </div>
</div>
{/if}
