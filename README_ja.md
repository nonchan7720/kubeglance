# kubeglance

[English](README.md) | 日本語

**クラスタのリソースを食っているのは誰か、スマホから10秒で分かる。**

kubeglance が答える問いはひとつです。*今、クラスタの CPU とメモリを使っているのは何で、それはこの30分ずっとそうなのか？* 4つの階層を、それぞれひとつの意思決定のために表示します。

| 階層 | 問い | 意思決定 |
|---|---|---|
| **Cluster** | 空き容量はどれだけ残っている？ | ワークロードを追加できるか |
| **Node** | 負荷は均等に散っている？ | リバランスすべきか |
| **Workload**（Deployment / StatefulSet / DaemonSet / CronJob） | このサービスの負荷は、Pod あたりいくつ？ | スケールすべきか |
| **Pod** | 単体で熱いのはどれ？ | まずどこを見るか |

すべての数値は **現在値 · 平均 · 中央値 · p95** として、ウィンドウ（5分〜24時間）で集計し、Pod の requests / limits やノードの allocatable に対するバーとして、片手で読めるように表示します。各バーの下にはウィンドウ内の推移を同じスケールで描いたスパークライン（request は破線、欠損は網掛け）があり、「この 30 分ずっとこうか？」が一目で分かります。ワークロードと Pod は namespace で絞り込み、CPU・メモリ・名前で並べ替えられます。

- **静的バイナリ1つ、Pod 1つ。** DB も Prometheus も Grafana も不要。データ源は metrics-server（k3s に同梱）だけ。
- **短期履歴はメモリ上に。** Pod とノードごとの固定長リングバッファ。100 Pod × 24時間 × 15秒で約 10 MB。[メモリ](#メモリ) を参照。
- **無停止更新・履歴欠損ゼロ。** レプリカ2台なら、起動中のレプリカが稼働中のレプリカから履歴をコピーしてから Ready になります。[更新時に履歴を保つ仕組み](#更新時に履歴を保つ仕組み) を参照。
- **モバイルファースト。** ホーム画面に置ける PWA。Tailscale や Cloudflare Tunnel、認証付き Ingress の裏に置いて、通勤電車から自宅ラボを確認。
- **日本語 / 英語 UI。** ブラウザの言語に従い、`?lang=ja` / `?lang=en` または EN/JA ボタンで切り替え（端末ごとに記憶）。
- **読み取り専用・極小フットプリント。** pods / nodes / replicasets / jobs の list/watch と metrics API のみ。要求は CPU 20m、メモリ 64Mi。

> ステータス: 初期段階。コア（コレクタ、履歴、4階層 API、UI、Helm chart）は動作します。次の作業は [TODO.md](TODO.md) を参照。

## インストール

```bash
helm install kubeglance oci://ghcr.io/nonchan7720/charts/kubeglance -n kubeglance --create-namespace
kubectl -n kubeglance port-forward svc/kubeglance 8080:80
# → http://127.0.0.1:8080
```

Argo CD なら、Application のソースを `oci://ghcr.io/nonchan7720/charts/kubeglance` か、このリポジトリの `charts/kubeglance` に向けてください。

要件: Kubernetes 1.30 以上（preStop の `sleep` アクションのため。古いクラスタでは `preStopSleepSeconds: 0`）と metrics-server。

linux / darwin / windows（amd64, arm64, armv7）のバイナリは [Releases](https://github.com/nonchan7720/kubeglance/releases) にあります。クラスタ外からは `kubeglance --kubeconfig ~/.kube/config` で動かせます。

## 設定

フラグ、または括弧内の環境変数。Helm chart は主要なものを `values.yaml` で公開しています。

| フラグ | 既定値 | 意味 |
|---|---|---|
| `--addr` (`KUBEGLANCE_ADDR`) | `:8080` | 待ち受けアドレス |
| `--interval` (`KUBEGLANCE_INTERVAL`) | `15s` | ポーリング間隔。metrics-server の更新は15秒なので、短くしても同じ値を2回読むだけ |
| `--retention` (`KUBEGLANCE_RETENTION`) | `24h` | メモリに保持する履歴。ウィンドウの上限 |
| `--peer-host` (`KUBEGLANCE_PEER_HOST`) | | レプリカの headless Service。空ならピアコピー無効 |
| `--peer-port` (`KUBEGLANCE_PEER_PORT`) | `8080` | ピアのポート |
| `--pod-ip` (`POD_IP`) | | 自分の Pod IP。ピアから除外（downward API） |
| `--snapshot` (`KUBEGLANCE_SNAPSHOT`) | | 履歴を永続化する gzip JSON ファイル（任意） |
| `--snapshot-every` (`KUBEGLANCE_SNAPSHOT_EVERY`) | `1m` | 書き出し間隔 |
| `--shutdown-grace` (`KUBEGLANCE_SHUTDOWN_GRACE`) | `5s` | SIGTERM 後もこの時間は応答し、新レプリカのコピー完了を待つ |
| `--kubeconfig` | | クラスタ外用。未指定なら in-cluster → `$KUBECONFIG` → `~/.kube/config` |
| `--log-level` (`KUBEGLANCE_LOG_LEVEL`) | `info` | `debug` / `info` / `warn` / `error`（stderr に JSON） |

## API

UI が表示するものはすべて `/api/v1` 配下の JSON です。各エンドポイントは `?window=30m`（既定）を受け付けます。

| パス | ビュー |
|---|---|
| `GET /api/v1/cluster` | 合計、allocatable、requests 合計、使用量の統計 |
| `GET /api/v1/nodes` | ノードごと: 使用量統計、allocatable 比、載っている Pod 数と requests。加えてノード間の偏り（spread, stddev） |
| `GET /api/v1/workloads?namespace=` | コントローラごと: Pod 数、requests、limits、合算した使用量統計、Pod あたり使用量、使用量/requests 比 |
| `GET /api/v1/pods?namespace=&node=&workload=ns/Kind/name` | Pod ごと: 使用量統計、使用量/limits 比、使用量/requests 比。ウィンドウ内に消えた Pod も含み、フラグ付き |
| `GET /api/v1/status` | バージョン、間隔、保持期間、コレクタの健全性 |
| `GET /healthz`, `GET /readyz` | liveness。readiness は最初のラウンドを収集してから |
| `GET /internal/snapshot` | 履歴全体（ピアがコピーするもの） |

統計は生サンプルから計算し、「平均の平均」は使いません。各統計オブジェクトの `coverage` はウィンドウのうちサンプルがある割合で、UI は欠損を隠さず表示します。

各行には描画用の `spark` オブジェクトも付きます。約 60 個のバケット（幅 `bucket`、`start` から。時計に揃えてあるので、更新しても形が変わらずにスライドします）で、各値はバケット内サンプルの平均、欠損は `null` です。描画専用で、統計はここから導きません。クライアントが `Accept-Encoding: gzip` を送ればレスポンスは gzip 圧縮されます。

## メモリ

1サンプルは16バイト（CPU ミリコア + メモリバイト。タイムスタンプはラウンドで共有）。15秒間隔なら24時間で 5,761 サンプル / エンティティ:

| Pod 数 | 履歴（24時間） |
|---|---|
| 50 | 約 5 MB |
| 100 | 約 9 MB |
| 300 | 約 28 MB |

これに Go ランタイム、HTTP サーバー、client-go のキャッシュ（10〜20 MB）が加わります。chart は 64Mi 要求 / 128Mi 上限で、Raspberry Pi の k3s にも収まります。`--retention` を短くすれば減り、ウィンドウ選択は保持期間で頭打ちになります。

## 更新時に履歴を保つ仕組み

metrics-server は瞬間値しか返さないので、ポーリングの空白はそのまま永遠の欠損です。kubeglance は DB なしでこれを避けます。

1. `replicas: 2`、`RollingUpdate` で `maxUnavailable: 0`、`maxSurge: 1`。
2. 新しい Pod は headless Service `<release>-peers` を解決し、他のレプリカから `/internal/snapshot` を取得してマージ。一度ポーリングしてから Ready になる。
3. それから古い Pod が終了。`preStopSleepSeconds` 待ち、SIGTERM 後も `--shutdown-grace` の間は応答し続けるので、進行中のコピーが完了する。
4. 両レプリカは同じ metrics-server を叩くので、履歴は構造的に同一。リーダーも合意も不要。

任意の `--snapshot` ファイル（chart の `snapshot.enabled`）は、全レプリカが同時に落ちた場合（クラスタ再起動）の保険です。更新を無停止にしているのはこれではなく、ピアコピーです。k3s のノードローカルストレージではそもそも無停止更新に使えません。

## アーキテクチャ

```
main.go              全体の配線
pkg/domain           サンプル、リングバッファ Series、Stats（avg / median / p95 / coverage）、Store、集約。Kubernetes 非依存
pkg/services         Collector（ポーリング → store）、Query（4つのビュー）、Bootstrap（ピア、ディスク）、Persister
pkg/infrastructure   k8s（metrics.k8s.io ポーリング、Pod → owner のインフォーマー、ノード）、peer（HTTP）、snapshot（ファイル）
pkg/interfaces       HTTP API と埋め込み UI（素の HTML / CSS / JS。ビルドなし、CDN なし）
charts/kubeglance    Helm chart
internal/devserver   `mise run dev`: クラスタなしで合成データの UI を表示
```

レイヤーは [depgraph](https://github.com/nonchan7720/depgraph)（`.depgraph.yml`）で、編集ごとのチェックは [claude-hook-gate](https://github.com/nonchan7720/claude-hook-gate)（`.claude/gate.yaml`）で強制しています。

## 開発

```bash
mise install         # Go, golangci-lint, helm, goreleaser（mise.toml で固定）
mise run ci          # lint (incl. govet) + test + helm lint + build
mise run dev         # 合成データの UI を http://127.0.0.1:8080 で
mise run run         # 現在の kubeconfig コンテキストに対して実行
mise run depgraph:watch   # 依存グラフと Findings（Neo4j 用に Docker が必要）
```

リリース: `main` の Conventional Commits から release-please がリリース PR を作り、マージすると `vX.Y.Z` タグが打たれ、goreleaser がバイナリ・マルチアーキイメージ（`ghcr.io/nonchan7720/kubeglance`）・Helm chart（`oci://ghcr.io/nonchan7720/charts/kubeglance`）を公開します。それ以外の `main` への push は `vX.Y.Z-dev.<sha>` のプレリリースになります。

## ライセンス

Apache-2.0。[LICENSE](LICENSE) を参照。
