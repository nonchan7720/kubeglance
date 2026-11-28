# Changelog

## [0.1.0](https://github.com/nonchan7720/kubeglance/compare/v0.1.0...v0.1.0) (2026-11-28)


### ci

* pin the first release with a Release-As commit footer, not the config ([ac840dd](https://github.com/nonchan7720/kubeglance/commit/ac840dd8dcf4f43f2abefb46e70fb690ea8d5c0f))


### Features

* /metrics for kubeglance itself ([#21](https://github.com/nonchan7720/kubeglance/issues/21)) ([6f60316](https://github.com/nonchan7720/kubeglance/commit/6f60316d7f55710a2da1ecd0fed9b0a9191a467b))
* alerts to a webhook when usage stays near the limit ([#33](https://github.com/nonchan7720/kubeglance/issues/33)) ([b91e678](https://github.com/nonchan7720/kubeglance/commit/b91e67812880472a8cdfc9962864a204ca771425))
* **chart:** PodDisruptionBudget, optional NetworkPolicy, effective config at startup ([#15](https://github.com/nonchan7720/kubeglance/issues/15)) ([ca226a1](https://github.com/nonchan7720/kubeglance/commit/ca226a174b62ada8700ab2c076fdc914e0fa4546))
* initial scaffold of kubeglance ([544ac56](https://github.com/nonchan7720/kubeglance/commit/544ac5673641882d8ea800faa96fdbaf33732913))
* optional token on the UI, the API and /metrics ([#22](https://github.com/nonchan7720/kubeglance/issues/22)) ([b49ee57](https://github.com/nonchan7720/kubeglance/commit/b49ee575bbc2f0b92bf6bd031c89969e11afd4cf))
* per-container breakdown on the Pod screen ([#14](https://github.com/nonchan7720/kubeglance/issues/14)) ([459ec3f](https://github.com/nonchan7720/kubeglance/commit/459ec3f9664be04c5df4be2ee60f2cd38f957c6d))
* poll interval follows metrics-server's resolution, also at runtime ([#17](https://github.com/nonchan7720/kubeglance/issues/17)) ([6f1979f](https://github.com/nonchan7720/kubeglance/commit/6f1979f422ff6f6cd3a35e18215c8bd04c462fd2))
* start kubeglance with a cobra command ([#10](https://github.com/nonchan7720/kubeglance/issues/10)) ([71b7f9f](https://github.com/nonchan7720/kubeglance/commit/71b7f9f795290cfcd46657e426c7561fc03b9a1a))
* **ui:** sparklines with gaps, namespace filter and sort toggle ([#13](https://github.com/nonchan7720/kubeglance/issues/13)) ([9aca740](https://github.com/nonchan7720/kubeglance/commit/9aca740ef508d967f85ca64ca9fa3ac8e6473f75))
* **ui:** the wordmark in the header, and a status dot that explains itself ([#31](https://github.com/nonchan7720/kubeglance/issues/31)) ([fc6f6dc](https://github.com/nonchan7720/kubeglance/commit/fc6f6dc3f9db31f9d42073d23217a40a08664c39))


### Bug Fixes

* **deps:** update module github.com/spf13/pflag to v1.1.0 ([#39](https://github.com/nonchan7720/kubeglance/issues/39)) ([dfc6383](https://github.com/nonchan7720/kubeglance/commit/dfc638310598e9cdc82b6771aed9988aa67ad69f))
* replicas record the same instants, and a replica is not Ready without the peer copy ([#16](https://github.com/nonchan7720/kubeglance/issues/16)) ([ca4ba73](https://github.com/nonchan7720/kubeglance/commit/ca4ba734afd6c14e466b063e0897b821d82e7059))
* store a sample in 16 bytes, as README "Memory" says (it took 40) ([#26](https://github.com/nonchan7720/kubeglance/issues/26)) ([7788ad5](https://github.com/nonchan7720/kubeglance/commit/7788ad55eeac702d4ccedf61efb45f283ec32c8c))
* stream the snapshot to a starting peer instead of building it in memory ([#27](https://github.com/nonchan7720/kubeglance/issues/27)) ([b91a2eb](https://github.com/nonchan7720/kubeglance/commit/b91a2eb99062c631b80d30b0fade45f83b0f8ffd))
* tell metrics-server's resolution from its samples, and stop reporting gaps that are not there ([#34](https://github.com/nonchan7720/kubeglance/issues/34)) ([6e4186a](https://github.com/nonchan7720/kubeglance/commit/6e4186a0d155e3dd1c8b6609c117fbcc8a89cf3e))
* **ui:** colour-blind-safe palette in the k8s-ecosystem look ([#24](https://github.com/nonchan7720/kubeglance/issues/24)) ([f1b1cca](https://github.com/nonchan7720/kubeglance/commit/f1b1ccaf86463c098dbdc679c705c0723b83a8be))


### Miscellaneous

* **deps:** update actions/create-github-app-token action to v3.4.0 ([#50](https://github.com/nonchan7720/kubeglance/issues/50)) ([5731e21](https://github.com/nonchan7720/kubeglance/commit/5731e2101dd099a5d0a77bba5a483b82bd66a1c7))
* **deps:** update amannn/action-semantic-pull-request action to v6.1.2 ([#36](https://github.com/nonchan7720/kubeglance/issues/36)) ([8f6f8f9](https://github.com/nonchan7720/kubeglance/commit/8f6f8f9322f491b486b01ee326ca7ce627bb219f))
* **deps:** update amannn/action-semantic-pull-request action to v6.1.3 ([#53](https://github.com/nonchan7720/kubeglance/issues/53)) ([e28aa57](https://github.com/nonchan7720/kubeglance/commit/e28aa57b8ded99c3ae4452bd5f1e88894ebb14d3))
* **deps:** update dependency aqua:goreleaser/goreleaser to v2.20.3 ([#51](https://github.com/nonchan7720/kubeglance/issues/51)) ([9ef8402](https://github.com/nonchan7720/kubeglance/commit/9ef8402eb476f3ad7bb09ab5161e54e2511de10e))
* **deps:** update dependency github:nonchan7720/depgraph to v0.1.11 ([#40](https://github.com/nonchan7720/kubeglance/issues/40)) ([746a372](https://github.com/nonchan7720/kubeglance/commit/746a3721351396a8d66854e76b6d81b00eb5f5b3))
* **deps:** update dependency github:nonchan7720/depgraph to v0.1.12 ([#42](https://github.com/nonchan7720/kubeglance/issues/42)) ([eeaf8a8](https://github.com/nonchan7720/kubeglance/commit/eeaf8a8f25bd8930b01a60b48c901694c97d3dbe))
* **deps:** update dependency github:nonchan7720/depgraph to v0.1.14 ([#46](https://github.com/nonchan7720/kubeglance/issues/46)) ([9eef09f](https://github.com/nonchan7720/kubeglance/commit/9eef09fede1590b96cf0b52c15b43338a349269b))
* **deps:** update docker/login-action action to v4.8.0 ([#38](https://github.com/nonchan7720/kubeglance/issues/38)) ([9756897](https://github.com/nonchan7720/kubeglance/commit/9756897b0bd3c1ada0336697c0de57bb875e825c))
* **deps:** update jdx/mise-action action to v5.2.10 ([#49](https://github.com/nonchan7720/kubeglance/issues/49)) ([adbb245](https://github.com/nonchan7720/kubeglance/commit/adbb24579b6930e5498fe098e01d013e8aeba553))
* **deps:** update jdx/mise-action action to v5.2.7 ([#37](https://github.com/nonchan7720/kubeglance/issues/37)) ([240304d](https://github.com/nonchan7720/kubeglance/commit/240304d415e8e127974d8e26c328404c74656927))
* **deps:** update jdx/mise-action action to v5.2.8 ([#44](https://github.com/nonchan7720/kubeglance/issues/44)) ([d679084](https://github.com/nonchan7720/kubeglance/commit/d67908417863c1605421418fd5201a61acc8fcc4))
* **deps:** update jdx/mise-action action to v5.2.9 ([#48](https://github.com/nonchan7720/kubeglance/issues/48)) ([3768088](https://github.com/nonchan7720/kubeglance/commit/37680881f47328c6782a0b1ea052960270bf73d0))
* **deps:** update neo4j docker tag to v5.26.33 ([#43](https://github.com/nonchan7720/kubeglance/issues/43)) ([49740ee](https://github.com/nonchan7720/kubeglance/commit/49740ee65bbf83518cc773c04322fdf6eec2a0b9))
* **deps:** update neo4j:5.26.32 docker digest to 42ef3eb ([#35](https://github.com/nonchan7720/kubeglance/issues/35)) ([1d89eaa](https://github.com/nonchan7720/kubeglance/commit/1d89eaa66dd13b0483ac309c8dd2b57e6d4058cf))
* **deps:** update toolchain ([#11](https://github.com/nonchan7720/kubeglance/issues/11)) ([c5e0935](https://github.com/nonchan7720/kubeglance/commit/c5e0935b05278c8251f2a203ae06b59d759b1f94))
* **deps:** update toolchain ([#41](https://github.com/nonchan7720/kubeglance/issues/41)) ([aa9bf01](https://github.com/nonchan7720/kubeglance/commit/aa9bf0188f3ef1f61ea7c0001435a0fe3b120128))
* **deps:** update toolchain ([#45](https://github.com/nonchan7720/kubeglance/issues/45)) ([c10e622](https://github.com/nonchan7720/kubeglance/commit/c10e622f13d11d23335408339b5e32c05d7b6562))


### Documentation

* kingfisher logo, UI icon and the chart icon ([#19](https://github.com/nonchan7720/kubeglance/issues/19)) ([393426b](https://github.com/nonchan7720/kubeglance/commit/393426bd2b7af83366a942789d255d11753f9f96))
* wordmark (the logo with the name as outlines) for the CNCF Landscape ([#25](https://github.com/nonchan7720/kubeglance/issues/25)) ([2a0020a](https://github.com/nonchan7720/kubeglance/commit/2a0020a3d757398f1a81622f7eb99158e1e7a396))
