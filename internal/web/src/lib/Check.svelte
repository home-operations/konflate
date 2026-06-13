<script lang="ts">
  import Icon from './Icon.svelte';
  import { mdiCheckCircle, mdiCloseCircleOutline, mdiCircleOutline } from './icons';
  import type { CheckRollup } from './types';

  // The PR head's rolled-up CI status, shown beside the PR title in the list and
  // the review header. success/failure/pending map to the ok/danger/warn-tinted
  // check / x / hollow-circle (.check-{state} in app.css). The caller renders this
  // only when pr.checks is present — a head with no checks shows nothing.
  let { checks, size = 14 }: { checks: CheckRollup; size?: number } = $props();

  const icon = $derived(
    checks.state === 'success'
      ? mdiCheckCircle
      : checks.state === 'failure'
        ? mdiCloseCircleOutline
        : mdiCircleOutline,
  );
  const title = $derived(
    checks.state === 'success'
      ? `Checks passed (${checks.passed}/${checks.total})`
      : checks.state === 'failure'
        ? `Checks failing — ${checks.failed} of ${checks.total} failed`
        : `Checks running (${checks.passed}/${checks.total} done)`,
  );
</script>

<span class="check check-{checks.state}" {title}>
  <Icon path={icon} size={size} />
</span>
