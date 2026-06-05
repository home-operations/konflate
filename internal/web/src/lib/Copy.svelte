<script lang="ts">
  // A small icon button that copies `text` to the clipboard and briefly flips
  // to a check on success. Used sparingly for values a reviewer commonly lifts
  // out (a head SHA, an image reference, a resource identifier).
  import Icon from './Icon.svelte';
  import { mdiContentCopy, mdiCheck } from './icons';

  interface Props {
    text: string;
    label?: string; // accessible label / tooltip, e.g. "Copy image reference"
    size?: number;
    icon?: string; // idle glyph (defaults to the copy icon); e.g. a terminal icon for a shell command
  }
  let { text, label = 'Copy', size = 13, icon = mdiContentCopy }: Props = $props();

  let copied = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      return; // clipboard unavailable (insecure context); fail quietly
    }
    copied = true;
    clearTimeout(timer);
    timer = setTimeout(() => (copied = false), 1200);
  }
</script>

<button
  type="button"
  class="copy-btn"
  class:copied
  onclick={copy}
  title={copied ? 'Copied!' : label}
  aria-label={label}
>
  <Icon path={copied ? mdiCheck : icon} {size} />
</button>
