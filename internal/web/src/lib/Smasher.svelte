<script lang="ts">
  // The loading mascot: a figure applying percussive maintenance to the
  // Kubernetes logo (when reconciliation is too slow). Pure CSS keyframes
  // (see "loading smasher" in app.css) — the arm swings, the logo jolts and
  // cracks, sparks fly on impact. Inherits currentColor like the other
  // loaders; sparks tint caution.
  import { kubernetesMark } from './icons';

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

  <!-- the long-suffering Kubernetes logo (the official heptagon-helm mark) -->
  <g class="smash-wheel">
    <path
      d={kubernetesMark.path}
      transform="translate(14 48) scale(1.3333)"
      fill="currentColor"
      stroke="none"
    />
    <!-- crack, revealed on impact — panel-colored so it reads through the fill -->
    <path class="smash-sparks smash-crack" d="M31 50 l3 6 l-5 5 l4 5" stroke-width="1.5" />
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
