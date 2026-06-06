<script lang="ts">
  // The loading mascot: a figure applying percussive maintenance to the
  // Kubernetes wheel (when reconciliation is too slow). Pure CSS keyframes
  // (see "loading smasher" in app.css) — the arm swings, the wheel jolts and
  // cracks, sparks fly on impact. Inherits currentColor like the other
  // loaders; sparks tint caution.
  let { size = 120, label = 'Loading' }: { size?: number; label?: string } = $props();
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
  <!-- ground line -->
  <line x1="6" y1="80" x2="114" y2="80" opacity="0.35" />

  <!-- the long-suffering Kubernetes wheel (the favicon, grounded) -->
  <g class="smash-wheel">
    <circle cx="30" cy="65" r="15" />
    <circle cx="30" cy="65" r="3.2" fill="currentColor" stroke="none" />
    <g stroke-width="2">
      <line x1="30" y1="54" x2="30" y2="59" />
      <line x1="30" y1="71" x2="30" y2="76" />
      <line x1="19" y1="65" x2="24" y2="65" />
      <line x1="36" y1="65" x2="41" y2="65" />
      <line x1="22.2" y1="57.2" x2="25.8" y2="60.8" />
      <line x1="34.2" y1="69.2" x2="37.8" y2="72.8" />
      <line x1="37.8" y1="57.2" x2="34.2" y2="60.8" />
      <line x1="25.8" y1="69.2" x2="22.2" y2="72.8" />
    </g>
    <!-- crack, revealed on impact -->
    <path class="smash-sparks" d="M34 52 l3 5 l-4 4" stroke-width="1.5" />
  </g>

  <!-- impact sparks -->
  <g class="smash-sparks spark-tint" stroke-width="2">
    <line x1="38" y1="42" x2="33" y2="36" />
    <line x1="44" y1="38" x2="44" y2="30" />
    <line x1="50" y1="42" x2="55" y2="36" />
  </g>

  <!-- the technician -->
  <circle cx="92" cy="26" r="6" />
  <line x1="92" y1="32" x2="88" y2="56" />
  <line x1="88" y1="56" x2="80" y2="78" />
  <line x1="88" y1="56" x2="96" y2="78" />
  <!-- swinging arm + hammer (pivot at the shoulder; impact pose as drawn) -->
  <g class="smash-arm">
    <line x1="90" y1="38" x2="64" y2="37" />
    <line x1="64" y1="37" x2="56" y2="36" />
    <rect x="49" y="29" width="9" height="16" rx="1.5" fill="currentColor" stroke="none" />
  </g>
</svg>
