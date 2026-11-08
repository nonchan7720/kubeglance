# <img src="docs/logo.svg" alt="" width="40" align="top"> kubeglance

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
- **スマホへのアラート（任意）。** コンテナがメモリ / CPU の limit 近く、またはノードが allocatable 近くにある時間が直近 10 分の半分以上になったら webhook に通知し、収まったらもう一度通知します。[アラート](#アラート) を参照。
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

レプリカが 2 つなら chart は PodDisruptionBudget（`minAvailable: 1`）も作るので、ノードの drain で両レプリカ（と履歴）が同時に消えることはありません。`networkPolicy.enabled: true` で NetworkPolicy を追加します。ingress は他のレプリカと `networkPolicy.ingressFrom`（Ingress コントローラ）から 8080 へ、egress は DNS・API サーバー（`networkPolicy.apiServer`。metrics API もここが提供）・レプリカへ。許可元はクラスタごとに違うので、既定ではオフです。

最初のログ行 `starting` には、実際に使われる設定（フラグ・環境変数・既定値を合わせたもの）が `config` オブジェクトとして入ります。

### アクセス

kubeglance はクラスタ内で何が動いているかを見せるので、インターネットに直接出さず、Tailscale・Cloudflare Access・認証付き Ingress 経由でアクセスしてください。それができない場合はトークンを設定します。

```bash
kubectl -n kubeglance create secret generic kubeglance-auth --from-literal=token="$(openssl rand -hex 24)"
helm upgrade kubeglance oci://ghcr.io/nonchan7720/charts/kubeglance -n kubeglance --reuse-values --set auth.existingSecret=kubeglance-auth
```

ブラウザは最初に一度だけユーザー名（何でもよい）とパスワード（トークン）を尋ねます。API クライアントや Prometheus は `Authorization: Bearer <token>` を送れます。`/healthz`・`/readyz`・Web マニフェスト・アイコンは、プローブとホーム画面への追加のために開いたままです。レプリカ同士は履歴コピーのときにトークンを送り合います。トークンはファイル（マウントした Secret）から読み、フラグでは渡せません。起動ログには設定の有無だけが出ます。

### アラート

既定ではオフです。webhook を設定すると、`--alert-window`（既定 10 分）のうち半分以上の時間、次の値が閾値（`--alert-memory`・`--alert-cpu`。既定 90%、0 でそのリソースを無効化）以上だったときに POST し、収まったとき（`resolved`。Pod やノードが消えたときも）にもう一度 POST します。

| ルール | 比べるもの | 意味 |
|---|---|---|
| `container-memory` | コンテナのメモリと、その limit | OOM kill が近い |
| `container-cpu` | コンテナの CPU と、その limit | スロットリングされている |
| `node-memory` / `node-cpu` | ノードの使用量と allocatable | ノードが埋まっている |

「現在値」ではなく「ウィンドウの半分」で判定するので、続いている問題は多少下がっても発火し、一瞬のスパイクでは発火せず、ウィンドウの大半が落ち着くまで解消になりません。ウィンドウ内の欠損サンプルは「閾値未満」として数えるので、欠損でアラートが遅れることはあっても、欠損が原因で発火することはありません。limit のないコンテナは比較しません。複数コンテナの Pod はコンテナごとに判定します（OOM kill はコンテナ単位のため）。

```bash
helm upgrade kubeglance oci://ghcr.io/nonchan7720/charts/kubeglance -n kubeglance --reuse-values \
  --set alerts.webhook=https://receiver.example.com/kubeglance \
  --set alerts.publicURL=https://kubeglance.example.ts.net   # アラートから該当の Pod / ノードへリンク
```

`--alert-format=json`（既定）は 1 回の評価で変わったものを 1 リクエスト `{"version": 1, "alerts": [...]}` で送ります。各アラートには `key`（ルールと対象）、`status`（`firing` / `resolved`）、`since`、`at`、`gone`、`verdict`（閾値、limit、ウィンドウの現在値 / 平均 / 中央値 / p95 と coverage）、そのまま使える `title` と `body`（`--alert-language=en|ja`）、`url` が入ります。`--alert-format=pushshell` は各アラートを [pushshell](https://github.com/nonchan7720/pushshell) のバックエンドへ通知として送ります（`POST /v1/notifications`、ログイン ID `kubeglance` 宛て。解消の通知はスマホ上で発火の通知を置き換えます）。一度に 4 件以上なら 1 件のまとめ通知にします。pushshell アプリの中で kubeglance の UI を開くと、UI が自動でそのログイン ID に端末を登録します。API キーなどのヘッダーは値ではなく Secret から渡します。

```bash
kubectl -n kubeglance create secret generic kubeglance-alerts --from-literal=headers='X-API-Key: ...'
helm upgrade ... --set alerts.format=pushshell --set alerts.webhook=https://push.example.com/v1/notifications \
  --set alerts.existingSecret=kubeglance-alerts --set alerts.headersKey=headers
```

URL 自体が認証情報になっている場合（Slack 形式の webhook）は、同じ Secret に入れて `alerts.webhook` の代わりに `alerts.urlKey` を指定してください。起動ログにはスキームとホストだけが出ます。`networkPolicy.enabled` のときは受信側への egress を追加します（`alerts.egress`）。

全レプリカが同じ履歴に対してルールを評価し、同じ判定になります。送信するのは Pod IP が最小の Ready なレプリカだけなので、2 レプリカでも二重に通知せず、API サーバーへの書き込み権限も要りません。そのレプリカがいなくなると、もう一方が送信済みのものを繰り返さずに引き継ぎます。送信に失敗したら次の評価で再送します。状態をメモリの外に持たないことによる制約が 1 つあります。稼働中のレプリカの横で起動したレプリカは、その時点で発火しているものは送信済みとみなします。そのため、アラートを初めて有効にした時点ですでに発火しているものは、いったん解消して再び発火するまで送られません。

linux / darwin / windows（amd64, arm64, armv7）のバイナリは [Releases](https://github.com/nonchan7720/kubeglance/releases) にあります。クラスタ外からは `kubeglance --kubeconfig ~/.kube/config` で動かせます。

## 設定

フラグ、または括弧内の環境変数。コマンドラインのフラグが環境変数より優先され、不正な値の環境変数は既定値に戻さず起動を止めます。`kubeglance --help` で一覧、`kubeglance --version` でバージョンを表示します。Helm chart は主要なものを `values.yaml` で公開しています。

| フラグ | 既定値 | 意味 |
|---|---|---|
| `--addr` (`KUBEGLANCE_ADDR`) | `:8080` | 待ち受けアドレス |
| `--interval` (`KUBEGLANCE_INTERVAL`) | 自動 | ポーリング間隔。既定では metrics-server の `--metric-resolution` に合わせる。毎回のポーリングで metrics-server が返す `window` から読む（k3s は 15 秒。マネージドでは別の値のこともある）。変更は 3 回連続で同じ値が報告されたら再起動なしで追従し、最初の値が分かるまでは 15 秒。変更をまたぐウィンドウの統計は、各区間をその区間の間隔で数える。解像度より短くしても同じ値を 2 回読むだけなので、間隔を固定したいときだけ指定する（指定するとその後は変わらない） |
| `--retention` (`KUBEGLANCE_RETENTION`) | `24h` | メモリに保持する履歴。ウィンドウの上限 |
| `--peer-host` (`KUBEGLANCE_PEER_HOST`) | | レプリカの headless Service。空ならピアコピー無効 |
| `--peer-port` (`KUBEGLANCE_PEER_PORT`) | `8080` | ピアのポート |
| `--pod-ip` (`POD_IP`) | | 自分の Pod IP。ピアから除外（downward API） |
| `--snapshot` (`KUBEGLANCE_SNAPSHOT`) | | 履歴を永続化する gzip JSON ファイル（任意） |
| `--snapshot-every` (`KUBEGLANCE_SNAPSHOT_EVERY`) | `1m` | 書き出し間隔 |
| `--shutdown-grace` (`KUBEGLANCE_SHUTDOWN_GRACE`) | `5s` | SIGTERM 後もこの時間は応答し、新レプリカのコピー完了を待つ |
| `--bootstrap-timeout` (`KUBEGLANCE_BOOTSTRAP_TIMEOUT`) | `2m` | ピアはいるのに 1 つも応答しないとき、起動中のレプリカが履歴コピーを再試行する時間。その間は Ready にならない |
| `--auth-token-file` (`KUBEGLANCE_AUTH_TOKEN_FILE`) | | UI・API・`/metrics`・ピアコピーに要求するトークンを書いたファイル。空なら無効。[アクセス](#アクセス) を参照 |
| `--alert-webhook` (`KUBEGLANCE_ALERT_WEBHOOK`) | | アラートを受け取る URL。空なら無効。[アラート](#アラート) を参照 |
| `--alert-format` (`KUBEGLANCE_ALERT_FORMAT`) | `json` | `json`（全アラートを 1 リクエストで）または `pushshell`（アラートごとに pushshell の通知） |
| `--alert-headers-file` (`KUBEGLANCE_ALERT_HEADERS_FILE`) | | 毎回送る `Name: value` 形式のヘッダー（API キーなど）を書いたファイル |
| `--alert-window` (`KUBEGLANCE_ALERT_WINDOW`) | `10m` | このウィンドウの半分以上が閾値以上ならルールが発火する |
| `--alert-memory` (`KUBEGLANCE_ALERT_MEMORY`) | `0.9` | メモリの閾値。コンテナの limit・ノードの allocatable に対する割合。`0` で無効 |
| `--alert-cpu` (`KUBEGLANCE_ALERT_CPU`) | `0.9` | CPU について同上 |
| `--alert-language` (`KUBEGLANCE_ALERT_LANGUAGE`) | `en` | アラート本文の言語: `en` / `ja` |
| `--public-url` (`KUBEGLANCE_PUBLIC_URL`) | | スマホから kubeglance に届く URL。アラートはそこの `?pod=namespace/name` / `?node=name` にリンクする |
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
| `GET /api/v1/pods/{namespace}/{name}` | 1 つの Pod と、その各コンテナ（spec 順）: コンテナ自身の requests / limits に対する使用量統計。履歴にない Pod は 404 |
| `GET /api/v1/status` | バージョン、間隔、保持期間、コレクタの健全性 |
| `GET /healthz`, `GET /readyz` | liveness。readiness はピアからのコピーと最初のラウンドの収集が済んでから |
| `GET /metrics` | kubeglance 自身の Prometheus 向けメトリクス: ポーリング回数・失敗数・所要時間・最終成功時刻・現在の間隔・履歴の大きさ（Pod・ノード・系列・サンプル数・容量）。`networkPolicy.enabled` のときは Prometheus を `networkPolicy.ingressFrom` に加える |
| `GET /internal/snapshot` | 履歴全体（ピアがコピーするもの） |

統計は生サンプルから計算し、「平均の平均」は使いません。各統計オブジェクトの `coverage` はウィンドウのうちサンプルがある割合で、UI は欠損を隠さず表示します。

各行には描画用の `spark` オブジェクトも付きます。約 60 個のバケット（幅 `bucket`、`start` から。時計に揃えてあるので、更新しても形が変わらずにスライドします）で、各値はバケット内サンプルの平均、欠損は `null` です。描画専用で、統計はここから導きません。クライアントが `Accept-Encoding: gzip` を送ればレスポンスは gzip 圧縮されます。

## メモリ

1サンプルは16バイトで保存します（秒単位の時刻、CPU ミリコア、メモリバイト。テストで16バイトを保っています）。15秒間隔なら24時間で 5,761、7日で 40,321 サンプル / エンティティです（ヒープの実測値）:

| Pod 数 | 履歴（24時間） | 履歴（7日） |
|---|---|---|
| 50 | 約 5 MB | 約 32 MB |
| 100 | 約 10 MB | 約 65 MB |
| 300 | 約 30 MB | 約 195 MB |

コンテナが 2 つ以上ある Pod は、Pod 画面のコンテナ別内訳のためにコンテナごとの系列を追加で持ちます（コンテナが 1 つの Pod は Pod の系列がそのままコンテナの系列です）。サイドカー 1 つの Pod なら系列は 1 本ではなく 3 本です。

これに Go ランタイム、HTTP サーバー、client-go のキャッシュ（10〜20 MB）が加わります。chart は 64Mi 要求 / 128Mi 上限で、Raspberry Pi の k3s にも収まります。`--retention` を短くすれば減り、ウィンドウ選択は保持期間で頭打ちになります。

データの間引き（ダウンサンプリング）はしません。どの期間の統計も生のサンプルから計算します。そのためメモリは `--retention` に比例して増えます。保持期間を伸ばすときは `resources.limits.memory` も一緒に上げてください（例: `retention: 7d` で 100 Pod なら、履歴が約 65 MB とランタイムの分なので `limits.memory: 192Mi`）。

起動するレプリカへ履歴を渡すときに増えるメモリは数 MB で、履歴をもう 1 つ丸ごと持つことはありません。スナップショットは送る側も受け取る側も Pod 1 つずつ流します。chart はメモリ上限の 90 % を Go ランタイムのソフト上限として渡すので、ゴミがコンテナの上限に届く前に GC が回収を強めます。ただし上限を保証するものではありません。履歴そのものは生きているメモリなので、`limits.memory` は上の表をもとに決めてください。

## 更新時に履歴を保つ仕組み

metrics-server は瞬間値しか返さないので、ポーリングの空白はそのまま永遠の欠損です。kubeglance は DB なしでこれを避けます。

1. `replicas: 2`、`RollingUpdate` で `maxUnavailable: 0`、`maxSurge: 1`。
2. 新しい Pod は headless Service `<release>-peers`（Ready なレプリカだけを返す）を解決し、他のレプリカ全部から `/internal/snapshot` を取得してマージ。一度ポーリングしてから Ready になる。ピアがいるのに 1 つも応答しなければ、空の履歴のまま Ready にはならず、ピアを引き直して `--bootstrap-timeout` まで再試行する。その間も `/healthz` は応答するので再起動されない。
3. それから古い Pod が終了。`preStopSleepSeconds` 待ち、SIGTERM 後も `--shutdown-grace` の間は応答し続けるので、進行中のコピーが完了する。
4. 両レプリカは同じ metrics-server を叩き、各サンプルはポーリングした瞬間ではなく、`--interval` の絶対時刻の刻み（15 秒なら `:00`, `:15`, `:30`, `:45`）に記録される。各レプリカは起動時刻に関係なく各刻みの中間でポーリングする。そのため 2 つのレプリカは同じ時刻を記録し、マージしても交互に重ならず重複が除かれる。リーダーも合意も不要。

任意の `--snapshot` ファイル（chart の `snapshot.enabled`）は、全レプリカが同時に落ちた場合（クラスタ再起動）の保険です。更新を無停止にしているのはこれではなく、ピアコピーです。k3s のノードローカルストレージではそもそも無停止更新に使えません。

## アーキテクチャ

```
main.go              全体の配線
pkg/domain           サンプル、リングバッファ Series、Stats（avg / median / p95 / coverage）、Store、集約、アラートのルール。Kubernetes 非依存
pkg/services         Collector（ポーリング → store）、Query（4つのビュー）、Bootstrap（ピア、ディスク）、Persister、Alerter
pkg/infrastructure   k8s（metrics.k8s.io ポーリング、Pod → owner のインフォーマー、ノード）、peer（HTTP、アラート送信役の選出）、snapshot（ファイル）、webhook（アラート）
pkg/interfaces       コマンドライン（cobra: フラグ、環境変数）と HTTP API・埋め込み UI（素の HTML / CSS / JS。ビルドなし、CDN なし）
charts/kubeglance    Helm chart
internal/devserver   `mise run dev`: クラスタなしで合成データの UI を表示
```

レイヤーは [depgraph](https://github.com/nonchan7720/depgraph)（`.depgraph.yml`）で、編集ごとのチェックは [claude-hook-gate](https://github.com/nonchan7720/claude-hook-gate)（`.claude/gate.yaml`）で強制しています。

## 開発

```bash
mise install         # Go, golangci-lint, helm, goreleaser（mise.toml で固定）
mise run ci          # lint (incl. govet) + test + helm lint + build + notices check
mise run notices     # 依存を追加したら THIRD_PARTY_NOTICES.md を再生成
mise run dev         # 合成データの UI を http://127.0.0.1:8080 で
mise run run         # 現在の kubeconfig コンテキストに対して実行
mise run depgraph:watch   # 依存グラフと Findings（Neo4j 用に Docker が必要）
```

リリース: `main` の Conventional Commits から release-please がリリース PR を作り、マージすると `vX.Y.Z` タグが打たれ、goreleaser がバイナリ・マルチアーキイメージ（`ghcr.io/nonchan7720/kubeglance`）・Helm chart（`oci://ghcr.io/nonchan7720/charts/kubeglance`）を公開します。それ以外の `main` への push は `vX.Y.Z-dev.<sha>` のプレリリースになります。

## ライセンス

Apache-2.0。[LICENSE](LICENSE) を参照。
