# Changelog

## [0.1.0](https://github.com/nonchan7720/kubeglance/compare/v0.1.0...v0.1.0) (2026-11-08)


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

* replicas record the same instants, and a replica is not Ready without the peer copy ([#16](https://github.com/nonchan7720/kubeglance/issues/16)) ([ca4ba73](https://github.com/nonchan7720/kubeglance/commit/ca4ba734afd6c14e466b063e0897b821d82e7059))
* store a sample in 16 bytes, as README "Memory" says (it took 40) ([#26](https://github.com/nonchan7720/kubeglance/issues/26)) ([7788ad5](https://github.com/nonchan7720/kubeglance/commit/7788ad55eeac702d4ccedf61efb45f283ec32c8c))
* stream the snapshot to a starting peer instead of building it in memory ([#27](https://github.com/nonchan7720/kubeglance/issues/27)) ([b91a2eb](https://github.com/nonchan7720/kubeglance/commit/b91a2eb99062c631b80d30b0fade45f83b0f8ffd))
* tell metrics-server's resolution from its samples, and stop reporting gaps that are not there ([#34](https://github.com/nonchan7720/kubeglance/issues/34)) ([6e4186a](https://github.com/nonchan7720/kubeglance/commit/6e4186a0d155e3dd1c8b6609c117fbcc8a89cf3e))
* **ui:** colour-blind-safe palette in the k8s-ecosystem look ([#24](https://github.com/nonchan7720/kubeglance/issues/24)) ([f1b1cca](https://github.com/nonchan7720/kubeglance/commit/f1b1ccaf86463c098dbdc679c705c0723b83a8be))


### Documentation

* kingfisher logo, UI icon and the chart icon ([#19](https://github.com/nonchan7720/kubeglance/issues/19)) ([393426b](https://github.com/nonchan7720/kubeglance/commit/393426bd2b7af83366a942789d255d11753f9f96))
* wordmark (the logo with the name as outlines) for the CNCF Landscape ([#25](https://github.com/nonchan7720/kubeglance/issues/25)) ([2a0020a](https://github.com/nonchan7720/kubeglance/commit/2a0020a3d757398f1a81622f7eb99158e1e7a396))
