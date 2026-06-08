<script lang="ts">
  // Render a CLI command with its flags (`--repo`, …) dimmed so the tokens read
  // as distinct in a cramped mono chip. Every space is full-width already, but a
  // short mid-height `--` optically attaches to the token before it, so
  // `merge 142 --repo x` reads as `142--repo`; colouring the flag makes the
  // boundary obvious without loosening the mono spacing.
  //
  // Split keeps the whitespace runs (the captured group) so spacing is intact.
  // Tokens render as plain text — auto-escaped, never {@html} — so a
  // forge-derived repo name can't inject markup.
  let { command }: { command: string } = $props();
  const tokens = $derived(command.split(/(\s+)/));
</script>

{#each tokens as t}{#if t.startsWith('-')}<span class="cmd-flag">{t}</span>{:else}{t}{/if}{/each}
