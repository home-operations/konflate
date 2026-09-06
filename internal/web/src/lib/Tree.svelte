<script lang="ts">
  import { router } from './router.svelte';
  import { store, diffIndex, openSel } from './store.svelte';
  import Icon from './Icon.svelte';
  import { mdiAlert, mdiAlertOctagonOutline, mdiFileDocumentOutline } from './icons';

  // d can briefly be null while a new diff loads — every use below tolerates it.
  const d = $derived(store.diff);
  // A null selection (bare #/pr/N) defaults to the Summary node.
  const sel = $derived(router.route.name === 'review' ? (router.route.sel ?? 'summary') : null);
  // Resource titles ("Kind ns/name") carrying a caution, and the total caution
  // count — both from the shared diff index (computed once per diff).
  const cautionLabels = $derived(diffIndex().cautionResources);
  const blockingLabels = $derived(diffIndex().blockingResources);
  const cautionCount = $derived(diffIndex().cautionCount);
  const blockingCount = $derived(diffIndex().blockingCount);

  function open(id: string) {
    if (router.route.name === 'review') openSel(router.route.pr, id);
  }
</script>

<div class="tree">
  <button
    class="tree-summary"
    class:selected={sel === 'summary'}
    aria-current={sel === 'summary' ? 'true' : undefined}
    onclick={() => open('summary')}
  >
    <Icon path={mdiFileDocumentOutline} size={14} />
    <span class="leaf-name">Summary</span>
    {#if blockingCount}
      <span class="summary-caution blocking"><Icon path={mdiAlertOctagonOutline} size={13} label="has a blocker" /></span>
    {:else if cautionCount}
      <span class="summary-caution"><Icon path={mdiAlert} size={13} label="has cautions" /></span>
    {/if}
  </button>

  {#each d?.tree ?? [] as parent}
    <div class="tree-parent">
      <div class="tree-parent-label">{parent.label}</div>
      {#each parent.kinds as kind}
        <div class="tree-kind">{kind.kind}</div>
        {#each kind.items as item}
          <button
            class="tree-item status-{item.status}"
            class:selected={item.id === sel}
            aria-current={item.id === sel ? 'true' : undefined}
            onclick={() => open(item.id)}
          >
            <span class="leaf-name">{item.name}</span>
            {#if blockingLabels.has(`${kind.kind} ${item.name}`)}
              <span class="leaf-blocking"><Icon path={mdiAlertOctagonOutline} size={13} label="has a blocker" /></span>
            {:else if cautionLabels.has(`${kind.kind} ${item.name}`)}
              <Icon path={mdiAlert} size={13} label="has a caution" />
            {/if}
            <span class="leaf-counts">
              {#if item.add}<span class="add">+{item.add}</span>{/if}
              {#if item.del}<span class="del">-{item.del}</span>{/if}
            </span>
          </button>
        {/each}
      {/each}
    </div>
  {/each}
</div>
