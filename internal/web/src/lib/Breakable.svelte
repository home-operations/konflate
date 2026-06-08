<script lang="ts">
  // Render text with a word-break opportunity (<wbr>) after every "/", so a long
  // slash-separated value — an image ref, a resource path — can wrap at the
  // slashes, with the slash sitting at the end of the line exactly as the browser
  // already wraps after a "-". Built from segments and never via {@html}, so
  // forge-controlled titles are never treated as markup, and <wbr> adds no text
  // (textContent, copy, and the title attribute are unchanged).
  let { text }: { text: string } = $props();

  // Keep each segment's trailing "/" so the break lands after it; drop empties
  // from leading/trailing/doubled slashes.
  const segments = $derived(
    text
      .split('/')
      .map((part, i, arr) => (i < arr.length - 1 ? `${part}/` : part))
      .filter(Boolean),
  );
</script>

{#each segments as seg, i}{seg}{#if i < segments.length - 1}<wbr />{/if}{/each}
