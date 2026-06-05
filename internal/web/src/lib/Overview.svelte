<script lang="ts">
  import { router } from './router.svelte';
  import { store, openDiffs, currentPR } from './store.svelte';
  import { isViewed } from './viewed.svelte';
  import Icon from './Icon.svelte';
  import Copy from './Copy.svelte';
  import {
    mdiAlertOctagon,
    mdiAlert,
    mdiPackageVariantClosed,
    mdiAlertCircleOutline,
    mdiCheckCircle,
    mdiCircleOutline,
  } from './icons';

  const d = $derived(store.diff!);
  const pr = $derived(currentPR());

  function open(id: string) {
    if (router.route.name === 'review') openDiffs(router.route.pr, id);
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
      <table class="img-table">
        <tbody>
          {#each d.images as img}
            <tr>
              <td class="img-name">{img.name}</td>
              <td class="img-ver from">{img.from || '∅'}</td>
              <td class="img-arrow">→</td>
              <td class="img-ver to">
                <span class="ver-copy"
                  >{img.to || '∅'}{#if img.to}<Copy
                      text={`${img.name}:${img.to}`}
                      label="Copy image reference"
                    />{/if}</span
                >
              </td>
              <td class="img-refs">{img.refs?.join(', ') ?? ''}</td>
            </tr>
          {/each}
        </tbody>
      </table>
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
            <Icon
              path={pr && isViewed(pr.number, pr.headSha, item.id) ? mdiCheckCircle : mdiCircleOutline}
              size={15}
            />
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
