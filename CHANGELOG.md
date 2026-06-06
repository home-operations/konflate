# Changelog

## [0.1.13](https://github.com/home-operations/konflate/compare/0.1.12...0.1.13) (2026-06-06)


### Features

* **ui:** redesign the diff review page ([#40](https://github.com/home-operations/konflate/issues/40)) ([5b78eaa](https://github.com/home-operations/konflate/commit/5b78eaab1cf90c207c515d8eaf9c7f9007d6d8c5))

## [0.1.12](https://github.com/home-operations/konflate/compare/0.1.11...0.1.12) (2026-06-06)


### Bug Fixes

* **ui:** palette focus containment, diff null guards, chroma CSS sync ([#38](https://github.com/home-operations/konflate/issues/38)) ([6e2e394](https://github.com/home-operations/konflate/commit/6e2e3946d80fbf10485d2980056bd4d32ccb92fa))

## [0.1.11](https://github.com/home-operations/konflate/compare/0.1.10...0.1.11) (2026-06-06)


### Features

* **diff:** warn on large change-sets and major image/chart bumps ([#37](https://github.com/home-operations/konflate/issues/37)) ([0cbe6e2](https://github.com/home-operations/konflate/commit/0cbe6e2b9b7d76351246b5b2ee5469c78fd613a9))
* **ui:** list sort direction toggle and a flash-free loading spinner ([f2786f1](https://github.com/home-operations/konflate/commit/f2786f106af5a190739255d2ebf360193a6695dd))


### Bug Fixes

* **ui:** manage modal focus and guard stale deep links ([#35](https://github.com/home-operations/konflate/issues/35)) ([fcef0c3](https://github.com/home-operations/konflate/commit/fcef0c325ea99254bb9579140897e4128413dc4e))

## [0.1.10](https://github.com/home-operations/konflate/compare/0.1.9...0.1.10) (2026-06-06)


### Features

* improve ui/ux ([#33](https://github.com/home-operations/konflate/issues/33)) ([9ebf3b2](https://github.com/home-operations/konflate/commit/9ebf3b2ab3b1acfed77c219e0c1cbd5f2af9c83c))

## [0.1.9](https://github.com/home-operations/konflate/compare/0.1.8...0.1.9) (2026-06-06)


### Performance Improvements

* **backend:** reuse a git mirror and enable flate's render caches ([#31](https://github.com/home-operations/konflate/issues/31)) ([07c9af0](https://github.com/home-operations/konflate/commit/07c9af05975805bd8794a84eb809522aa52f05ba))

## [0.1.8](https://github.com/home-operations/konflate/compare/0.1.7...0.1.8) (2026-06-06)


### Features

* **ui:** merge Overview/Diffs tabs into a single-page review ([#28](https://github.com/home-operations/konflate/issues/28)) ([b8e74f3](https://github.com/home-operations/konflate/commit/b8e74f37d57cc2914ed910baff44d25db38ed5a7))


### Bug Fixes

* **ui:** match the auto-refresh pill to the icon buttons on mobile ([#27](https://github.com/home-operations/konflate/issues/27)) ([21f8e99](https://github.com/home-operations/konflate/commit/21f8e995d80ea85f97d9dfafd37270a296570c68))

## [0.1.7](https://github.com/home-operations/konflate/compare/0.1.6...0.1.7) (2026-06-06)


### Features

* **ui:** mobile polish & Overview restyle; remove the viewed feature ([#23](https://github.com/home-operations/konflate/issues/23)) ([352c717](https://github.com/home-operations/konflate/commit/352c717362329830379c180b8bc91b821c5c4329))
* **ui:** move the PR number into the meta row for a readable title ([#21](https://github.com/home-operations/konflate/issues/21)) ([104466c](https://github.com/home-operations/konflate/commit/104466ce1b7759bb48f7fbb62fb3ee3a6e094ac2))

## [0.1.6](https://github.com/home-operations/konflate/compare/0.1.5...0.1.6) (2026-06-05)


### Features

* add copyable per-forge merge command ([#19](https://github.com/home-operations/konflate/issues/19)) ([227b748](https://github.com/home-operations/konflate/commit/227b748328b9a707f259b38478abb775caf6033c))


### Bug Fixes

* bust asset caches on deploy with content-hashed bundles ([#20](https://github.com/home-operations/konflate/issues/20)) ([f878c8d](https://github.com/home-operations/konflate/commit/f878c8d63a5b3d189e1c4e19a1a113761420afa1))
* keep the last-good diff when a re-render fails ([#17](https://github.com/home-operations/konflate/issues/17)) ([aff3193](https://github.com/home-operations/konflate/commit/aff3193fffe9f581211fd94dbb0ca26e08f8655d))

## [0.1.5](https://github.com/home-operations/konflate/compare/0.1.4...0.1.5) (2026-06-05)


### Features

* author avatars via a signed same-origin proxy ([#16](https://github.com/home-operations/konflate/issues/16)) ([8447cfc](https://github.com/home-operations/konflate/commit/8447cfcc6e55319f96b0795a48f26b26a5593ff6))
* log each PR refresh, and accept form-urlencoded webhooks ([#13](https://github.com/home-operations/konflate/issues/13)) ([65ee381](https://github.com/home-operations/konflate/commit/65ee38111d3c13ae1ba643486deb1c2592ac34a0))
* **ui:** pill-style list summary (open/danger/failed/rendering/merged) + review-header meta icons ([#15](https://github.com/home-operations/konflate/issues/15)) ([bbfb4a1](https://github.com/home-operations/konflate/commit/bbfb4a19ebb8890ee9e0a893f0be53d27dfc5068))

## [0.1.4](https://github.com/home-operations/konflate/compare/0.1.3...0.1.4) (2026-06-05)


### Features

* **ui:** landing health summary, non-default base tag, and meta icons ([#11](https://github.com/home-operations/konflate/issues/11)) ([9f351be](https://github.com/home-operations/konflate/commit/9f351be366e62565dc0ca951748f2f7a8a591d68))

## [0.1.3](https://github.com/home-operations/konflate/compare/0.1.2...0.1.3) (2026-06-05)


### Features

* changed-only rendering, plus merged-PR fix and flate bump ([#9](https://github.com/home-operations/konflate/issues/9)) ([d60d46d](https://github.com/home-operations/konflate/commit/d60d46dea8c95a3968556cd0e284ca5d4d9b68f6))

## [0.1.2](https://github.com/home-operations/konflate/compare/0.1.1...0.1.2) (2026-06-05)


### Features

* **ui:** restack image changes and shorten digests ([#8](https://github.com/home-operations/konflate/issues/8)) ([0e7247f](https://github.com/home-operations/konflate/commit/0e7247fbca2a0dec63bddc401d06a387640b0aaf))


### Bug Fixes

* add more thundering herd things ([9edadc1](https://github.com/home-operations/konflate/commit/9edadc1ad7b6010f99ef95d0f104f61997ed77da))
* implement GOMEMLIMIT logic ([9200f93](https://github.com/home-operations/konflate/commit/9200f93882b0a6ccb4d83c844590ab96e4a9dc62))

## [0.1.1](https://github.com/home-operations/konflate/compare/0.1.0...0.1.1) (2026-06-05)


### Bug Fixes

* disable k8s service links update gitlab provider ([87a1d25](https://github.com/home-operations/konflate/commit/87a1d25fc18baadde41879b9e3f20f646a819660))

## 0.1.0 (2026-06-05)


### Bug Fixes

* **deps:** update dependency svelte (5.56.1 → 5.56.2) ([#1](https://github.com/home-operations/konflate/issues/1)) ([16f4e5a](https://github.com/home-operations/konflate/commit/16f4e5aa563017095601585d6516ade192e44779))
