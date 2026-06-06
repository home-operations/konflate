<script lang="ts">
  import { router } from './router.svelte';
  import { store, openSel } from './store.svelte';
  import Icon from './Icon.svelte';
  import { mdiAlertOctagon, mdiFileDocumentOutline } from './icons';

  // d can briefly be null while a new diff loads — every use below tolerates it.
  const d = $derived(store.diff);
  // A null selection (bare #/pr/N) defaults to the Summary node.
  const sel = $derived(router.route.name === 'review' ? (router.route.sel ?? 'summary') : null);
  // Resources carrying a danger warning, keyed by their "Kind ns/name" label.
  const dangerLabels = $derived(
    new Set((d?.warnings ?? []).filter((w) => w.level === 'danger').map((w) => w.resource)),
  );
  const dangerCount = $derived((d?.warnings ?? []).filter((w) => w.level === 'danger').length);

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
    {#if dangerCount}
      <span class="summary-danger"><Icon path={mdiAlertOctagon} size={13} label="has danger warnings" /></span>
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
            {#if dangerLabels.has(`${kind.kind} ${item.name}`)}
              <Icon path={mdiAlertOctagon} size={13} label="has a danger warning" />
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
