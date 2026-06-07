// Central icon registry. Importing only the names we use keeps the bundle
// small (Rollup tree-shakes @mdi/js and simple-icons to just these paths).

export {
  mdiThemeLightDark,
  mdiWeatherNight,
  mdiWhiteBalanceSunny,
  mdiAlertOctagon,
  mdiAlert,
  mdiPackageVariantClosed,
  mdiAlertCircleOutline,
  mdiOpenInNew,
  mdiArrowLeft,
  mdiChevronLeft,
  mdiChevronRight,
  mdiChevronDown,
  mdiSourceMerge,
  mdiCheck,
  mdiContentCopy,
  mdiConsoleLine,
  mdiCheckCircle,
  mdiCheckCircleOutline,
  mdiCircleOutline,
  mdiClose,
  mdiHistory,
  mdiKeyboardOutline,
  mdiMagnify,
  mdiFileDocumentOutline,
  mdiClockOutline,
  mdiRefresh,
  mdiAccountOutline,
  mdiTagOutline,
  mdiScaleBalance,
  mdiCopyright,
  mdiSortVariant,
  mdiSortAscending,
  mdiSortDescending,
  mdiSourceBranch,
  mdiSourcePull,
  mdiSourceFork,
  mdiFilterOutline,
  mdiTrayFull,
  mdiUnfoldMoreHorizontal,
  mdiUnfoldLessHorizontal,
  mdiLoading,
} from '@mdi/js';

import { siDiscord, siForgejo, siGithub, siGitlab, siKubernetes } from 'simple-icons';

interface BrandIcon {
  path: string;
  hex: string;
  title: string;
}

// Forge brand logos, keyed by ForgeKind (matches api.Meta.forge).
export const forgeIcon: Record<string, BrandIcon> = {
  github: siGithub,
  gitlab: siGitlab,
  forgejo: siForgejo,
};

// The GitHub mark for the "konflate on GitHub" footer link (konflate is hosted
// there, independent of which forge the reviewed repo lives on).
export const githubMark: BrandIcon = siGithub;
export const discordMark: BrandIcon = siDiscord;
// The official heptagon-helm mark, for the loading mascot's smashee.
export const kubernetesMark: BrandIcon = siKubernetes;
export const KONFLATE_REPO_URL = 'https://github.com/home-operations/konflate';
export const DISCORD_URL = 'https://discord.gg/home-operations';
export const LICENSE_URL = 'https://github.com/home-operations/konflate/blob/main/LICENSE';
