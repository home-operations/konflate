<script lang="ts">
  // The render-in-progress mascot: a figure applying percussive maintenance to
  // a monitor. Pure CSS keyframes (see "loading smasher" in app.css) — the arm
  // swings, the screen jolts and cracks, sparks fly on impact. Inherits
  // currentColor like the other loaders; sparks tint caution.
  let { size = 120, label = 'Rendering' }: { size?: number; label?: string } = $props();
</script>

<svg
  class="smasher"
  width={size}
  height={size * 0.75}
  viewBox="0 0 120 90"
  fill="none"
  stroke="currentColor"
  stroke-width="2.5"
  stroke-linecap="round"
  stroke-linejoin="round"
  role="img"
  aria-label={label}
>
  <!-- desk line -->
  <line x1="6" y1="80" x2="114" y2="80" opacity="0.35" />

  <!-- the long-suffering monitor -->
  <g class="smash-screen">
    <rect x="14" y="36" width="32" height="22" rx="2" />
    <line x1="30" y1="58" x2="30" y2="66" />
    <line x1="22" y1="66" x2="38" y2="66" />
    <!-- crack, revealed on impact -->
    <path class="smash-sparks" d="M27 40 l4 6 l-5 4 l4 4" stroke-width="1.5" />
  </g>

  <!-- impact sparks -->
  <g class="smash-sparks spark-tint" stroke-width="2">
    <line x1="46" y1="28" x2="41" y2="22" />
    <line x1="52" y1="24" x2="52" y2="16" />
    <line x1="58" y1="28" x2="63" y2="22" />
  </g>

  <!-- the technician -->
  <circle cx="92" cy="26" r="6" />
  <line x1="92" y1="32" x2="88" y2="56" />
  <line x1="88" y1="56" x2="80" y2="78" />
  <line x1="88" y1="56" x2="96" y2="78" />
  <!-- swinging arm + hammer (pivot at the shoulder; impact pose as drawn) -->
  <g class="smash-arm">
    <line x1="90" y1="38" x2="64" y2="35" />
    <line x1="64" y1="35" x2="56" y2="34" />
    <rect x="49" y="26" width="9" height="16" rx="1.5" fill="currentColor" stroke="none" />
  </g>
</svg>
