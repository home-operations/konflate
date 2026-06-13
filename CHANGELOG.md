# Changelog

## [0.2.18](https://github.com/home-operations/konflate/compare/0.2.17...0.2.18) (2026-06-13)


### Features

* opt-in read-only MCP endpoint for AI agents ([#220](https://github.com/home-operations/konflate/issues/220)) ([15d3805](https://github.com/home-operations/konflate/commit/15d3805e9d118f297eabcc83a31854eacc08186c))

## [0.2.17](https://github.com/home-operations/konflate/compare/0.2.16...0.2.17) (2026-06-13)


### Features

* surface forge rate-limit / poll failures instead of an empty list ([#216](https://github.com/home-operations/konflate/issues/216)) ([8444e25](https://github.com/home-operations/konflate/commit/8444e25d9a83629202e8b9d29603bbade0f38aec))
* **ui:** review-header + summary layout polish ([#219](https://github.com/home-operations/konflate/issues/219)) ([fcb99d8](https://github.com/home-operations/konflate/commit/fcb99d8d48f6f6fb792c2bc5f7d2c109770cbc7b))
* **writeback:** post a GitHub Check Run for the render verdict ([#218](https://github.com/home-operations/konflate/issues/218)) ([654c566](https://github.com/home-operations/konflate/commit/654c56693c6b9f8b59cbc99bf3a92bd3af15bc21))

## [0.2.16](https://github.com/home-operations/konflate/compare/0.2.15...0.2.16) (2026-06-13)


### Bug Fixes

* **provider:** authenticate GitHub reads with the App, not just write-back ([#213](https://github.com/home-operations/konflate/issues/213)) ([2b0e477](https://github.com/home-operations/konflate/commit/2b0e477575ea9f21f1a88a5783f22bde4ec2d9ce))

## [0.2.15](https://github.com/home-operations/konflate/compare/0.2.14...0.2.15) (2026-06-12)


### Features

* **ui:** always show all four summary columns, with empty-state placeholders ([#209](https://github.com/home-operations/konflate/issues/209)) ([4f24ba4](https://github.com/home-operations/konflate/commit/4f24ba4b455eab6ce38e4af8bd4f3fea4ef50cda))

## [0.2.14](https://github.com/home-operations/konflate/compare/0.2.13...0.2.14) (2026-06-12)


### Bug Fixes

* **writeback:** skip the comment edit when the body is unchanged ([#207](https://github.com/home-operations/konflate/issues/207)) ([b276060](https://github.com/home-operations/konflate/commit/b27606035715abf72035d581df5acef0cada3b7f))

## [0.2.13](https://github.com/home-operations/konflate/compare/0.2.12...0.2.13) (2026-06-12)


### Features

* **ui:** tidy the summary pane — blast radius + image changes ([#202](https://github.com/home-operations/konflate/issues/202)) ([ca6c569](https://github.com/home-operations/konflate/commit/ca6c5699a9049f105b09f7072efaad2609f10659))
* **writeback:** configurable status check name (default "Konflate") ([#206](https://github.com/home-operations/konflate/issues/206)) ([8f52865](https://github.com/home-operations/konflate/commit/8f528653f839d63e18aa5aad5416a5f267e11a89))


### Bug Fixes

* **deps:** update kubernetes monorepo (v0.36.1 → v0.36.2) ([#200](https://github.com/home-operations/konflate/issues/200)) ([f959d27](https://github.com/home-operations/konflate/commit/f959d2743f68f245a0f47e43b86ac47e5198a34a))
* **deps:** update tailwindcss monorepo (4.3.0 → 4.3.1) ([#201](https://github.com/home-operations/konflate/issues/201)) ([582f466](https://github.com/home-operations/konflate/commit/582f466522e09cecb57d954df1e396b884467fc7))
* **writeback:** serialize write-backs per PR to stop duplicate comments ([#204](https://github.com/home-operations/konflate/issues/204)) ([7c58403](https://github.com/home-operations/konflate/commit/7c5840346fb37a02a206f896881a23019ea0ac2c))


### Code Refactoring

* **provider:** hand-roll GitHub App auth on jwt/v5; drop ghinstallation ([#205](https://github.com/home-operations/konflate/issues/205)) ([27bc350](https://github.com/home-operations/konflate/commit/27bc350d4eb7170bc695838e7eead56eaf09d916))

## [0.2.12](https://github.com/home-operations/konflate/compare/0.2.11...0.2.12) (2026-06-12)


### Features

* **chart:** availability knobs (PDB, priorityClass, grace, startup probe) ([#189](https://github.com/home-operations/konflate/issues/189)) ([8c113b2](https://github.com/home-operations/konflate/commit/8c113b2b1036057499608988118f927aaada72b1))
* **deps:** update module gitlab.com/gitlab-org/api/client-go/v2 (v2.38.0 → v2.39.0) ([#193](https://github.com/home-operations/konflate/issues/193)) ([6f5c8d6](https://github.com/home-operations/konflate/commit/6f5c8d6bf19fc9575d18b2fc46c46d5f2d1ee536))
* **ui:** declutter the diff overview and review header ([#197](https://github.com/home-operations/konflate/issues/197)) ([bbd1f7b](https://github.com/home-operations/konflate/commit/bbd1f7b931ef24b67c20367037006d5959f481f3))
* **writeback:** auto-detect the GitHub App installation ([#196](https://github.com/home-operations/konflate/issues/196)) ([0ee3b04](https://github.com/home-operations/konflate/commit/0ee3b04681fa421a9bb4ece4b5cfe0b50bc7b019))
* **writeback:** verify the write credential at startup ([#195](https://github.com/home-operations/konflate/issues/195)) ([72bbd38](https://github.com/home-operations/konflate/commit/72bbd38402b5c1afd61c41dc033877475c889ea3))


### Bug Fixes

* **deps:** update module github.com/home-operations/flate (v0.4.4 → v0.4.5) ([#192](https://github.com/home-operations/konflate/issues/192)) ([c3f2f71](https://github.com/home-operations/konflate/commit/c3f2f71875f77464f80c7ff78226e934263ea88a))
* **security:** harden write-back and the avatar proxy ([#198](https://github.com/home-operations/konflate/issues/198)) ([981f115](https://github.com/home-operations/konflate/commit/981f115e5893be6186938a110b43017d87de1104))

## [0.2.11](https://github.com/home-operations/konflate/compare/0.2.10...0.2.11) (2026-06-12)


### Features

* CI check status in the PR list (poll + webhook push) ([#185](https://github.com/home-operations/konflate/issues/185)) ([cb9487c](https://github.com/home-operations/konflate/commit/cb9487c8066c9cf425ad1d6fadce96e9288a8067))
* **ui:** compact the review summary layout ([#184](https://github.com/home-operations/konflate/issues/184)) ([e699892](https://github.com/home-operations/konflate/commit/e699892b4e17c4c4a25d4348e782203d8929cf72))
* **writeback:** opt-in commit-status write-back (PAT or GitHub App) ([#186](https://github.com/home-operations/konflate/issues/186)) ([a80f782](https://github.com/home-operations/konflate/commit/a80f782fc651850c4860fc520ccbfb26f3ad02c0))
* **writeback:** post/update a PR comment with the rendered summary ([#187](https://github.com/home-operations/konflate/issues/187)) ([0a9909d](https://github.com/home-operations/konflate/commit/0a9909de0aa8cdd687bad66cfb9c5cb2dc67028c))
* **writeback:** retry transient forge writes with backoff ([#188](https://github.com/home-operations/konflate/issues/188)) ([fb8f2c5](https://github.com/home-operations/konflate/commit/fb8f2c5a7657114846965f7dc87c869071eb8932))


### Bug Fixes

* **images:** merge rename-split add/remove pairs into one transition ([#182](https://github.com/home-operations/konflate/issues/182)) ([5108871](https://github.com/home-operations/konflate/commit/51088716b97096dfe42583245bfce7ac8df21772))

## [0.2.10](https://github.com/home-operations/konflate/compare/0.2.9...0.2.10) (2026-06-12)


### Bug Fixes

* **deps:** update module github.com/home-operations/flate (v0.4.1 → v0.4.3) ([#178](https://github.com/home-operations/konflate/issues/178)) ([d8b1b7a](https://github.com/home-operations/konflate/commit/d8b1b7ac414e3aa3404852495a6713293f0fad40))
* **deps:** update module github.com/home-operations/flate (v0.4.3 → v0.4.4) ([#181](https://github.com/home-operations/konflate/issues/181)) ([d2096b9](https://github.com/home-operations/konflate/commit/d2096b96d723de82c4cde721f644ccbba3a38054))

## [0.2.9](https://github.com/home-operations/konflate/compare/0.2.8...0.2.9) (2026-06-11)


### Features

* **blast-radius:** rank dependsOn blast radius + flag dangling deps ([#177](https://github.com/home-operations/konflate/issues/177)) ([7cbdaf0](https://github.com/home-operations/konflate/commit/7cbdaf02d34f95e3d4ad9cae98d611bf59717636))

## [0.2.8](https://github.com/home-operations/konflate/compare/0.2.7...0.2.8) (2026-06-10)


### Features

* **lint:** caution on immutable-field changes that wedge the apply ([#173](https://github.com/home-operations/konflate/issues/173)) ([18a8c72](https://github.com/home-operations/konflate/commit/18a8c7248142f2558dadc1499d71d2aa376b6562))
* **lint:** Flux-semantic cautions — suspend awareness and prune semantics ([#176](https://github.com/home-operations/konflate/issues/176)) ([1de1051](https://github.com/home-operations/konflate/commit/1de105162da5b69a1f0b21f537e34de5f3558a92))
* **ui:** find-in-diff search ('/' on the review) ([#175](https://github.com/home-operations/konflate/issues/175)) ([9e945fe](https://github.com/home-operations/konflate/commit/9e945fe5b9efe5a3d4989fea4341d2c574343b37))

## [0.2.7](https://github.com/home-operations/konflate/compare/0.2.6...0.2.7) (2026-06-10)


### Features

* **deps:** update module gitlab.com/gitlab-org/api/client-go/v2 (v2.37.0 → v2.38.0) ([#172](https://github.com/home-operations/konflate/issues/172)) ([177aa22](https://github.com/home-operations/konflate/commit/177aa226924c8c07ad8b626b8cd4de428e18845d))


### Bug Fixes

* **ui:** give the card meta-row pills one explicit height ([#171](https://github.com/home-operations/konflate/issues/171)) ([cc33e6e](https://github.com/home-operations/konflate/commit/cc33e6e1f280e481d5f5cbb9d0aa4c7a22ef338c))
* **ui:** level the review top bars and close the sticky-header seam ([#168](https://github.com/home-operations/konflate/issues/168)) ([b85e1c6](https://github.com/home-operations/konflate/commit/b85e1c613e59451095e29b5945332e584863bddd))

## [0.2.6](https://github.com/home-operations/konflate/compare/0.2.5...0.2.6) (2026-06-10)


### Features

* **deps:** update module gitlab.com/gitlab-org/api/client-go/v2 (v2.36.3 → v2.37.0) ([#165](https://github.com/home-operations/konflate/issues/165)) ([57e10fb](https://github.com/home-operations/konflate/commit/57e10fba22cf5bc8cb8f7d87e86e5901980b83dc))
* **engine:** adopt flate v0.3.4 — in-memory stages, deterministic renders ([#166](https://github.com/home-operations/konflate/issues/166)) ([9219dcd](https://github.com/home-operations/konflate/commit/9219dcd63e9027827b86db19e06731488648f77f))

## [0.2.5](https://github.com/home-operations/konflate/compare/0.2.4...0.2.5) (2026-06-10)


### Features

* **web:** drop the #&lt;number&gt; text from PR-list rows, keep the forge icon ([#158](https://github.com/home-operations/konflate/issues/158)) ([ab9ebb4](https://github.com/home-operations/konflate/commit/ab9ebb4ddd5dc5c3f9174d27b2d3324ed55e9f50))


### Documentation

* **readme:** document the multi-cluster-monorepo workaround ([#159](https://github.com/home-operations/konflate/issues/159)) ([83f3367](https://github.com/home-operations/konflate/commit/83f336789ff762eac7f172ebb371cb7eadff6486))

## [0.2.4](https://github.com/home-operations/konflate/compare/0.2.3...0.2.4) (2026-06-10)


### Bug Fixes

* **chart:** document and enforce konflate's single-instance design ([#157](https://github.com/home-operations/konflate/issues/157)) ([3511971](https://github.com/home-operations/konflate/commit/3511971b32f52f3afbefc05ecaa219a620cd924a))
* **web:** make status:hidden work in the list filter and palette ([#154](https://github.com/home-operations/konflate/issues/154)) ([0a3382e](https://github.com/home-operations/konflate/commit/0a3382e128044fbd4d179370de7a5b14f0bc9289))


### Performance Improvements

* **gitclone:** stream blobs and dedupe MkdirAll in extractTree ([#155](https://github.com/home-operations/konflate/issues/155)) ([42571ec](https://github.com/home-operations/konflate/commit/42571ec284958b357b671e1433e787241d0d377e))

## [0.2.3](https://github.com/home-operations/konflate/compare/0.2.2...0.2.3) (2026-06-10)


### Bug Fixes

* **chart:** tpl-render existingSecret/existingClaim/serviceAccount.name/prFilterExpr ([#142](https://github.com/home-operations/konflate/issues/142)) ([f79c9f0](https://github.com/home-operations/konflate/commit/f79c9f0e29bcf338e4f7a2e95c905ed398b85599))
* **config,engine:** key the bare mirror and persisted state by repository ([#150](https://github.com/home-operations/konflate/issues/150)) ([137448b](https://github.com/home-operations/konflate/commit/137448bec2e79ebb17f7580fa7f2039618f1a4fa))
* **config,server:** make KONFLATE_REFRESH_INTERVAL=0 disable polling, not hot-loop ([#151](https://github.com/home-operations/konflate/issues/151)) ([f462d6e](https://github.com/home-operations/konflate/commit/f462d6ec29fe25fbc4ad7e4e1287d9d835b747f0))
* **config:** normalize an explicit cloud host (github://github.com/... → api.github.com) ([#146](https://github.com/home-operations/konflate/issues/146)) ([f808896](https://github.com/home-operations/konflate/commit/f8088968af27e361dcd5ce3de9dd3c970a01d552))
* **diff:** drop type-only "changed" resources that render an empty panel ([#152](https://github.com/home-operations/konflate/issues/152)) ([a4c559f](https://github.com/home-operations/konflate/commit/a4c559f8b98c97327f59e5e2269415703762a72d))
* **engine:** sweep the source cache at startup and surface silent sweep errors ([#141](https://github.com/home-operations/konflate/issues/141)) ([0f2298a](https://github.com/home-operations/konflate/commit/0f2298ac5cfd6940dd90a02246605e5083ba9a1e))
* **gitclone:** repack the bare mirror once packfiles accumulate ([#149](https://github.com/home-operations/konflate/issues/149)) ([4e6ec03](https://github.com/home-operations/konflate/commit/4e6ec03f1327ffb816fa19f5c4fc7f76c30dbd1c))
* **provider:** paginate ListPRs across all pages (github/gitlab/forgejo) ([#147](https://github.com/home-operations/konflate/issues/147)) ([462e64d](https://github.com/home-operations/konflate/commit/462e64df42e5f0d4019289a230c5df98f4ec7f90))
* **server,provider:** reap a deleted PR/MR instead of looping on a 404 forever ([#148](https://github.com/home-operations/konflate/issues/148)) ([beeec59](https://github.com/home-operations/konflate/commit/beeec599ce6a5fa29435db9d6e9afaef9bec87ea))
* **server:** coalesce webhook-triggered relists into a single worker ([#153](https://github.com/home-operations/konflate/issues/153)) ([77357f1](https://github.com/home-operations/konflate/commit/77357f1b794b8fb90cf65fd34ac8d5f4ad0563b7))
* **server:** render a PR that becomes filter-allowed without a new push ([#143](https://github.com/home-operations/konflate/issues/143)) ([1b83911](https://github.com/home-operations/konflate/commit/1b8391113043a41bbfffca85526d877ceeb5e029))


### Performance Improvements

* **server:** marshal the diff off the store write lock; commit the digest after save ([#144](https://github.com/home-operations/konflate/issues/144)) ([c37084c](https://github.com/home-operations/konflate/commit/c37084ca4cc00c626db7432dbf1f4d846bd73ec9))

## [0.2.2](https://github.com/home-operations/konflate/compare/0.2.1...0.2.2) (2026-06-09)


### Features

* **mise:** update tool cosign (3.0.6 → 3.1.1) ([#114](https://github.com/home-operations/konflate/issues/114)) ([7468262](https://github.com/home-operations/konflate/commit/7468262ba082f24e7b0e5c8a4dfffcd8e95ae12c))


### Bug Fixes

* **deps:** update module gitlab.com/gitlab-org/api/client-go/v2 (v2.36.2 → v2.36.3) ([#113](https://github.com/home-operations/konflate/issues/113)) ([19bcedb](https://github.com/home-operations/konflate/commit/19bcedb95468db309450100db40ac55d390db192))
* PR-filter data-loss, image-collapse, CronJob lint, and shutdown-lifecycle fixes ([#137](https://github.com/home-operations/konflate/issues/137)) ([5ac45d5](https://github.com/home-operations/konflate/commit/5ac45d546f26509739cc9f7ce7cd88ea7eb48bf7))


### Performance Improvements

* **server:** ETag/304 for the diff endpoint and unescaped JSON bodies ([#140](https://github.com/home-operations/konflate/issues/140)) ([de6e0fe](https://github.com/home-operations/konflate/commit/de6e0feb85519671d41d679388fa6b59d82db1d7))

## [0.2.1](https://github.com/home-operations/konflate/compare/0.2.0...0.2.1) (2026-06-09)


### Bug Fixes

* **engine:** don't show phantom removals/cautions when a parent's render times out ([#111](https://github.com/home-operations/konflate/issues/111)) ([c1a6bd6](https://github.com/home-operations/konflate/commit/c1a6bd60e251d28ef1e777c2998b39e111ed7501))

## [0.2.0](https://github.com/home-operations/konflate/compare/0.1.29...0.2.0) (2026-06-09)


### ⚠ BREAKING CHANGES

* **config:** KONFLATE_RENDER_FORK_PRS — explicit, default-closed fork gate ([#109](https://github.com/home-operations/konflate/issues/109))

### Features

* **config:** KONFLATE_RENDER_FORK_PRS — explicit, default-closed fork gate ([#109](https://github.com/home-operations/konflate/issues/109)) ([9b06417](https://github.com/home-operations/konflate/commit/9b064174cb7262ff99aba602462bc84f2299e0f0))
* **server,ui:** track filter-excluded PRs in a "hidden" pill; drop the merged collapsible ([#108](https://github.com/home-operations/konflate/issues/108)) ([e463bdf](https://github.com/home-operations/konflate/commit/e463bdfd31d136bf89bb8a4f7742a71ccc337262))
* **ui:** forge PR link (with the PR number) + icon-only render state ([#106](https://github.com/home-operations/konflate/issues/106)) ([d85eeab](https://github.com/home-operations/konflate/commit/d85eeab2d5c8e7775aec86375bcba4d26b5f5df3))

## [0.1.29](https://github.com/home-operations/konflate/compare/0.1.28...0.1.29) (2026-06-09)


### Features

* **mise:** update tool oxfmt (0.53.0 → 0.54.0) ([#94](https://github.com/home-operations/konflate/issues/94)) ([66b583a](https://github.com/home-operations/konflate/commit/66b583abdf2d2f75fce524ca680fdd28ac89b8c6))


### Bug Fixes

* **deps:** update module gitlab.com/gitlab-org/api/client-go/v2 (v2.36.1 → v2.36.2) ([#105](https://github.com/home-operations/konflate/issues/105)) ([2fec8ed](https://github.com/home-operations/konflate/commit/2fec8ed1c85fedd29fa9ef3a27c016f14ccace62))
* **server:** polish the summary PR comment ([#103](https://github.com/home-operations/konflate/issues/103)) ([9d4835d](https://github.com/home-operations/konflate/commit/9d4835d474766a140e5c65ad529c1d8de9e12879))
* **server:** simplify the PR-comment summary heading and caution header ([#101](https://github.com/home-operations/konflate/issues/101)) ([8084d11](https://github.com/home-operations/konflate/commit/8084d11936ae5170c98954fa1018e39174ded79f))


### Performance Improvements

* **config:** scale the in-memory Helm template cache by render concurrency ([#104](https://github.com/home-operations/konflate/issues/104)) ([a488c6b](https://github.com/home-operations/konflate/commit/a488c6bf46d3604ac52b2213f22a369cc4b017a6))

## [0.1.28](https://github.com/home-operations/konflate/compare/0.1.27...0.1.28) (2026-06-09)


### Features

* **server:** X-Konflate-Render-Status header on the summary endpoint ([#99](https://github.com/home-operations/konflate/issues/99)) ([582dd2f](https://github.com/home-operations/konflate/commit/582dd2fafaa4e41a82454a25d3db8a8902330ad0))

## [0.1.27](https://github.com/home-operations/konflate/compare/0.1.26...0.1.27) (2026-06-08)


### Features

* **config:** KONFLATE_PR_FILTER_EXPR — CEL PR filter (replaces label allowlist + fork toggle) ([#96](https://github.com/home-operations/konflate/issues/96)) ([cf287c6](https://github.com/home-operations/konflate/commit/cf287c64677235ea4eca8048a11fe96c1e5085f8))
* **server:** persist rendered diffs across restarts ([#98](https://github.com/home-operations/konflate/issues/98)) ([60d3523](https://github.com/home-operations/konflate/commit/60d352393262c400e9334ad409a2cf53deba2f6a))

## [0.1.26](https://github.com/home-operations/konflate/compare/0.1.25...0.1.26) (2026-06-08)


### Bug Fixes

* **ui:** mobile diff-header overflow + unified click-to-copy merge command ([#93](https://github.com/home-operations/konflate/issues/93)) ([cab20b7](https://github.com/home-operations/konflate/commit/cab20b7f246d4e274c3805d8f77b3ed812be49bf))

## [0.1.25](https://github.com/home-operations/konflate/compare/0.1.24...0.1.25) (2026-06-08)


### Bug Fixes

* **ui:** review header on one row + merge command on the Summary bar ([#91](https://github.com/home-operations/konflate/issues/91)) ([e9a6fa8](https://github.com/home-operations/konflate/commit/e9a6fa8fa285da6781f470d7facdad0a4cab891a))

## [0.1.24](https://github.com/home-operations/konflate/compare/0.1.23...0.1.24) (2026-06-08)


### Features

* **api:** 503 + Retry-After for still-rendering Markdown summaries ([#88](https://github.com/home-operations/konflate/issues/88)) ([2a28557](https://github.com/home-operations/konflate/commit/2a28557f371b0f5f10540b071edfd10df0371488))
* **config:** KONFLATE_PR_LABELS — track only PRs with allowlisted labels ([#90](https://github.com/home-operations/konflate/issues/90)) ([3e293bd](https://github.com/home-operations/konflate/commit/3e293bd9d25bb1b964c65c4e2ed007462051c734))
* **deps:** update module golang.org/x/sync (v0.20.0 → v0.21.0) ([#87](https://github.com/home-operations/konflate/issues/87)) ([72dfb77](https://github.com/home-operations/konflate/commit/72dfb77e3894d06c3b9695877789c36fba91b350))

## [0.1.23](https://github.com/home-operations/konflate/compare/0.1.22...0.1.23) (2026-06-08)


### Features

* **api:** serve the PR summary as Markdown for CI comments ([#85](https://github.com/home-operations/konflate/issues/85)) ([64a4ddb](https://github.com/home-operations/konflate/commit/64a4ddbe19f97565b8d3c961d6b4fc07928570b4))

## [0.1.22](https://github.com/home-operations/konflate/compare/0.1.21...0.1.22) (2026-06-08)


### Features

* **ui:** PR-list polish — open pill, expandable rows, expand-all, scroll-to-top ([#83](https://github.com/home-operations/konflate/issues/83)) ([c2869ea](https://github.com/home-operations/konflate/commit/c2869ea90343d8e3c87e20645c09238de62b44db))

## [0.1.21](https://github.com/home-operations/konflate/compare/0.1.20...0.1.21) (2026-06-08)


### Features

* **ui:** installable PWA, plus a quieter PR list & review ([#81](https://github.com/home-operations/konflate/issues/81)) ([4b291cb](https://github.com/home-operations/konflate/commit/4b291cbb03ca3daeb1c47d728c376e6e9d78f0c7))

## [0.1.20](https://github.com/home-operations/konflate/compare/0.1.19...0.1.20) (2026-06-08)


### Features

* bound render resource use (cache GC, shallow clones, diff cap, memory limit) ([#75](https://github.com/home-operations/konflate/issues/75)) ([ca4ecf1](https://github.com/home-operations/konflate/commit/ca4ecf119f3ce7f06bae32cbfca5046564df0576))
* **chart:** optional NetworkPolicy (default/cilium/calico, off by default) ([#79](https://github.com/home-operations/konflate/issues/79)) ([89aa416](https://github.com/home-operations/konflate/commit/89aa416eaee64f09ae469f54d878d8012705eea9))
* gate rendering of fork PRs behind KONFLATE_RENDER_FORK_PRS ([#74](https://github.com/home-operations/konflate/issues/74)) ([33f3d18](https://github.com/home-operations/konflate/commit/33f3d18ae40e29881582c6e7ed9542304b2e7c9b))
* **github-release:** update release helm-unittest/helm-unittest (v1.0.3 → v1.1.1) ([#71](https://github.com/home-operations/konflate/issues/71)) ([89cb894](https://github.com/home-operations/konflate/commit/89cb89451bd6825285bf7846d9d16de0e2ef48ba))
* **ui:** risk-first list triage — clean/images filters + clean flag ([#77](https://github.com/home-operations/konflate/issues/77)) ([986c1db](https://github.com/home-operations/konflate/commit/986c1dbe0c8dd9451762524c0d25b7da719ff5c4))


### Bug Fixes

* **deps:** update module gitlab.com/gitlab-org/api/client-go/v2 (v2.36.0 → v2.36.1) ([#70](https://github.com/home-operations/konflate/issues/70)) ([739465c](https://github.com/home-operations/konflate/commit/739465c449837565638ab5283487d5b4c679edf8))
* render fork PR heads via the forge pull ref ([#72](https://github.com/home-operations/konflate/issues/72)) ([cafb481](https://github.com/home-operations/konflate/commit/cafb4810434f4d67ec9ba2645d23ab8633af9cc8))

## [0.1.19](https://github.com/home-operations/konflate/compare/0.1.18...0.1.19) (2026-06-07)


### Features

* **container:** update image mirror.gcr.io/busybox (1.37.0 → 1.38.0) ([#63](https://github.com/home-operations/konflate/issues/63)) ([ecbbe60](https://github.com/home-operations/konflate/commit/ecbbe6096652e0c0daf74c1f4a4bea1be8b88018))


### Bug Fixes

* **chart:** pin the helm-test image as tag@digest so renovate updates both ([#67](https://github.com/home-operations/konflate/issues/67)) ([8dda6df](https://github.com/home-operations/konflate/commit/8dda6df0786d3f225d9a53ae9027579ecb17574b))
* **deps:** update dependency svelte (5.56.2 → 5.56.3) ([#64](https://github.com/home-operations/konflate/issues/64)) ([af21e0f](https://github.com/home-operations/konflate/commit/af21e0f1a1265152436053e1f0e680653bb05260))

## [0.1.18](https://github.com/home-operations/konflate/compare/0.1.17...0.1.18) (2026-06-07)


### Features

* **chart:** digest pinning, generated README + values schema, tpl values, and hardening ([#56](https://github.com/home-operations/konflate/issues/56)) ([7db7929](https://github.com/home-operations/konflate/commit/7db7929f8bac36ad704ad449d3880da4d225856d))

## [0.1.17](https://github.com/home-operations/konflate/compare/0.1.16...0.1.17) (2026-06-07)


### Bug Fixes

* **deps:** update dependency simple-icons (16.23.0 → 16.23.0) ([#57](https://github.com/home-operations/konflate/issues/57)) ([cdd8896](https://github.com/home-operations/konflate/commit/cdd8896164f268da91dfb6009fa5964950793f3b))
* **engine:** adopt flate v0.3.3 Tree API — make clusterPath work ([#59](https://github.com/home-operations/konflate/issues/59)) ([3cf815f](https://github.com/home-operations/konflate/commit/3cf815f502af0ccb9d64134fa09a8816cc00de2e))

## [0.1.16](https://github.com/home-operations/konflate/compare/0.1.15...0.1.16) (2026-06-07)


### Features

* **deps:** update dependency simple-icons (16.22.0 → 16.23.0) ([#50](https://github.com/home-operations/konflate/issues/50)) ([7fa1b4c](https://github.com/home-operations/konflate/commit/7fa1b4c289f777f08983b90c083645e0cbecfd38))


### Bug Fixes

* **deps:** update module github.com/home-operations/flate (v0.3.2-0.20260607015718-c639cb85dfa0 → v0.3.2) ([#49](https://github.com/home-operations/konflate/issues/49)) ([a7ba802](https://github.com/home-operations/konflate/commit/a7ba80213b82cb67da4c62e1fe11773fefb501a1))
* **engine:** surface per-resource render failures instead of aborting the diff ([#52](https://github.com/home-operations/konflate/issues/52)) ([1ec5699](https://github.com/home-operations/konflate/commit/1ec56997fa37a3c863cdd6046176e85d7192ca18))
* **ui:** on mobile, scroll the review so the diff gets the full screen ([#55](https://github.com/home-operations/konflate/issues/55)) ([c1a0a67](https://github.com/home-operations/konflate/commit/c1a0a67f0889f7145a6381d960027100f198c259))

## [0.1.15](https://github.com/home-operations/konflate/compare/0.1.14...0.1.15) (2026-06-07)


### Code Refactoring

* **engine:** adopt flate's SDK diff surface, drop the duplicated pipeline ([#47](https://github.com/home-operations/konflate/issues/47)) ([7d7c5ba](https://github.com/home-operations/konflate/commit/7d7c5ba22d00a12088ed5ba336ef536ea0289ce2))

## [0.1.14](https://github.com/home-operations/konflate/compare/0.1.13...0.1.14) (2026-06-07)


### Features

* **ui:** bundle Geist + Geist Mono; sans for prose, mono for code ([#43](https://github.com/home-operations/konflate/issues/43)) ([1a51a1d](https://github.com/home-operations/konflate/commit/1a51a1d58731f18a939b540791eb8abfac03ee2b))


### Performance Improvements

* **ui:** lazy-mount diff tables as they near the viewport ([#46](https://github.com/home-operations/konflate/issues/46)) ([e798156](https://github.com/home-operations/konflate/commit/e798156502adaa14198a499c89e31e095289e8ea))

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
