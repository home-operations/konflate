<script lang="ts">
  // The project's Kubernetes-helm wheel (the favicon), rotating — a thematic
  // "reconciling" loader for in-flight renders. Inherits the surrounding text
  // colour via currentColor, so it tints muted in lists and accent at full size.
  let { size = 18, label }: { size?: number; label?: string } = $props();
</script>

<svg
  class="kspin"
  width={size}
  height={size}
  viewBox="0 0 32 32"
  fill="none"
  role={label ? 'img' : undefined}
  aria-label={label}
  aria-hidden={label ? undefined : 'true'}
>
  <circle cx="16" cy="16" r="13" stroke="currentColor" stroke-width="2.5" opacity="0.25" />
  <circle cx="16" cy="16" r="3.2" fill="currentColor" />
  <g stroke="currentColor" stroke-width="2.5" stroke-linecap="round">
    <line x1="16" y1="2.5" x2="16" y2="8" />
    <line x1="16" y1="24" x2="16" y2="29.5" />
    <line x1="2.5" y1="16" x2="8" y2="16" />
    <line x1="24" y1="16" x2="29.5" y2="16" />
    <line x1="6.5" y1="6.5" x2="10.4" y2="10.4" />
    <line x1="21.6" y1="21.6" x2="25.5" y2="25.5" />
    <line x1="25.5" y1="6.5" x2="21.6" y2="10.4" />
    <line x1="10.4" y1="21.6" x2="6.5" y2="25.5" />
  </g>
</svg>
