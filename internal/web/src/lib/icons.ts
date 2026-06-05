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
  mdiCheckCircle,
  mdiCircleOutline,
  mdiFileDocumentOutline,
  mdiClockOutline,
  mdiAccountOutline,
  mdiTagOutline,
  mdiSourceBranch,
  mdiTrayFull,
  mdiUnfoldMoreHorizontal,
  mdiUnfoldLessHorizontal,
  mdiLoading,
} from '@mdi/js';

import { siForgejo, siGithub, siGitlab } from 'simple-icons';

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

// The GitHub mark for the "konflate on GitHub" link (konflate is hosted there,
// independent of which forge the reviewed repo lives on).
export const githubMark: BrandIcon = siGithub;
export const KONFLATE_REPO_URL = 'https://github.com/home-operations/konflate';
