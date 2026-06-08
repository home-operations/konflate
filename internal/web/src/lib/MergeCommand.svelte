<script lang="ts">
  // The "copy to merge" command, shown identically in the diff overview header
  // and the PR-list folded preview: a terminal icon, the command in a mono chip,
  // and a copy button. The chip itself is also a copy button — clicking anywhere
  // on the command copies it.
  //
  // Flags (`--repo`, …) render dimmed so the tokens read as distinct in the
  // cramped chip (a short mid-height `--` otherwise optically attaches to the
  // token before it: `merge 142 --repo` looks like `142--repo`). Tokens render
  // as plain text — auto-escaped, never {@html} — so a forge-derived repo name
  // can't inject markup. The split keeps the whitespace runs so spacing is intact.
  import { onDestroy } from 'svelte';
  import Icon from './Icon.svelte';
  import { mdiConsoleLine, mdiContentCopy, mdiCheck } from './icons';

  let { command }: { command: string } = $props();
  const tokens = $derived(command.split(/(\s+)/));

  // Mirrors Copy.svelte: write to the clipboard, then flip to a check for a beat.
  let copied = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;
  onDestroy(() => clearTimeout(timer));
  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
    } catch {
      return; // clipboard unavailable (insecure context); fail quietly
    }
    copied = true;
    clearTimeout(timer);
    timer = setTimeout(() => (copied = false), 1200);
  }
</script>

<div class="merge-cmd">
  <Icon path={mdiConsoleLine} size={14} />
  <!-- The chip is a button: its accessible name is the command text it holds. -->
  <button type="button" class="merge-cmd-text" onclick={copy} title={copied ? 'Copied!' : 'Click to copy'}
    >{#each tokens as t}{#if t.startsWith('-')}<span class="cmd-flag">{t}</span>{:else}{t}{/if}{/each}</button
  >
  <button
    type="button"
    class="copy-btn"
    class:copied
    onclick={copy}
    title={copied ? 'Copied!' : 'Copy merge command'}
    aria-label="Copy merge command"
  >
    <Icon path={copied ? mdiCheck : mdiContentCopy} size={13} />
  </button>
</div>
