<script lang="ts">
  import { router } from './router.svelte';
  import { store, openDiffs, currentPR } from './store.svelte';
  import { isViewed } from './viewed.svelte';
  import Icon from './Icon.svelte';
  import { mdiCheckCircle, mdiCircleOutline, mdiAlertOctagon } from './icons';

  const d = $derived(store.diff!);
  const pr = $derived(currentPR());
  const sel = $derived(router.route.name === 'review' ? router.route.resource : null);
  // Resources carrying a danger warning, keyed by their "Kind ns/name" label.
  const dangerLabels = $derived(
    new Set((d.warnings ?? []).filter((w) => w.level === 'danger').map((w) => w.resource)),
  );

  function open(id: string) {
    if (router.route.name === 'review') openDiffs(router.route.pr, id);
  }
</script>

<div class="tree">
  {#each d.tree ?? [] as parent}
    <div class="tree-parent">
      <div class="tree-parent-label">{parent.label}</div>
      {#each parent.kinds as kind}
        <div class="tree-kind">{kind.kind}</div>
        {#each kind.items as item}
          <button class="tree-item status-{item.status}" class:selected={item.id === sel} onclick={() => open(item.id)}>
            <Icon
              path={pr && isViewed(pr.number, pr.headSha, item.id) ? mdiCheckCircle : mdiCircleOutline}
              size={14}
            />
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
