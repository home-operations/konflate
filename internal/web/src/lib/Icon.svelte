<script lang="ts">
  // Renders a 24×24 SVG path (from @mdi/js or simple-icons) inline, inheriting
  // the surrounding text color. Tree-shaken: only imported icon paths ship.
  interface Props {
    path: string;
    size?: number;
    label?: string; // when set, the icon is meaningful (role=img); otherwise decorative
    spin?: boolean;
  }
  let { path, size = 18, label, spin = false }: Props = $props();
</script>

<svg
  class="icon"
  class:spin
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill="currentColor"
  role={label ? 'img' : undefined}
  aria-label={label}
  aria-hidden={label ? undefined : 'true'}
>
  <path d={path} />
</svg>
