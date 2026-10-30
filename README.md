# vmapi

libvirt の上に置く、VM 単位の薄い HTTP API。VM の作成・一覧・起動・停止・削除と
スナップショットを扱う。VM は誰からも見え、操作できるのは持ち主と管理者だけである。

- `vmapi` — API サーバー。devbox の apps のコンテナで動き、devbox proxy の後ろに置く
- `vmctl` — 人間のシェルとスクリプト向けの CLI

API の契約は `api/openapi.yaml`、設計の決定は `docs/adr/`、設定ファイルは
`docs/config.md` にある。

## サーバー `vmapi`

```bash
vmapi --config ~/.config/vmapi/config.yaml
```

- `--config` を省くと `$XDG_CONFIG_HOME/vmapi/config.yaml`、無ければ `~/.config/vmapi/config.yaml` を読む。項目は `docs/config.md`。
- 設定に誤りがあれば起動しない。誤りはすべて項目の名前を添えて標準エラーに出る。
- ログは JSON で標準エラーに出る。リクエストごとにメソッド・パス・状態・利用者を記録する。`user_data` は記録しない。
- libvirt には最初のリクエストのときにつなぐ。切れたら次のリクエストでつなぎ直す。届かないあいだ、API は `unavailable`（503）を返す。
- `SIGTERM` を受けると、処理中のリクエストを最大 30 秒待ってから止まる。
- 書き込みのタイムアウトは設けていない。イメージから最初の VM を作るときは、イメージの取得が終わるまで応答しない（ADR 0001・0004）。

実行には、`qemu-img`（`qemu-utils`）と `xorriso` が `PATH` にあること、libvirt のソケットに書き込めること、ディスクの置き場にホストと同じパスで書き込めることが要る。コンテナのイメージ（`ghcr.io/yuanying/vmapi`）には前の二つが入っている。後の二つは compose で与える（ADR 0008）。

### 受け付けるリクエスト

vmapi は `X-Devbox-User` の名前を利用者とし、それが無いリクエストを `unauthenticated`（401）で拒む。このヘッダは proxy が付ける。vmapi のポートはホストに公開せず、proxy からだけ届くように置く（ADR 0002・0008）。

利用者は誰でも、すべての VM とそのスナップショットを見られる。起動・停止・削除と、スナップショットの作成・削除・復元は、VM の持ち主と設定の `admins` に挙げた管理者だけができる。それ以外の利用者の操作は `permission_denied`（403）で拒み、libvirt には何も送らない（ADR 0002）。

### 画面

`/` は、すべての VM を、持ち主・IP・状態とともに並べる。利用者が操作できる VM（自分の VM。管理者にはすべての VM）の行にだけ、起動・停止・削除のボタンがある。停止はゲストにシャットダウンを頼むだけで、止まるのを待たない。作成は画面ではなく API・`vmctl`・codemode で行う。

ブラウザは proxy の cookie で認証されるので、よそのサイトのページからでも cookie 付きの `POST` を送れてしまう。vmapi は、ブラウザがよそのオリジンから送ったと示すリクエスト（`Sec-Fetch-Site` が `same-origin`・`none` 以外、または `Origin` のホストが `Host` と違うもの）のうち `GET`・`HEAD`・`OPTIONS` 以外を、画面と API の区別なく `permission_denied`（403）で拒む。どちらのヘッダも付けない `vmctl` や `curl` は影響を受けない（ADR 0010）。

## CLI `vmctl`

### 接続先と token

| 何を | 決め方（先にあるものが優先） |
|---|---|
| 接続先 | `--server`、`VMCTL_SERVER`、既定の `https://vm.poissonnerie.dev` |
| token | `--token-file` で指したファイル、`VMCTL_TOKEN`、`VMCTL_TOKEN_FILE` で指したファイル、既定の `$XDG_CONFIG_HOME/vmctl/token`（無ければ `~/.config/vmctl/token`） |

- token は `devbox-proxy token --service vm --user <名前>` で発行した proxy の Bearer token である。その名前が、作る VM の持ち主になる。
- token のファイルは、持ち主以外が読めると使わない。`chmod 600` にしておく。末尾の改行は無視する。
- token を直接渡すフラグは無い。プロセスの一覧やシェルの履歴に残るからである。
- token は出力にもエラーにも出さない。サーバーの応答に token が含まれていても伏せる。

```bash
mkdir -p ~/.config/vmctl
install -m 600 /dev/null ~/.config/vmctl/token
# 発行した token をこのファイルに書き込む
```

### 使い方

```bash
vmctl whoami                       # vmapi が自分を誰とみなしているか
vmctl images                       # 選べるイメージ
vmctl create k8s-cp1 --image noble --vcpus 4 --memory-mib 8192 --disk-gib 40 \
  --user-data cloud-config.yaml    # 作成して起動。払い出した IP が表示される
vmctl create k8s-w1 --image noble --ip 192.168.40.21 --user-data - < cloud-config.yaml
vmctl list                         # すべての VM。OWNER の列に持ち主が出る
vmctl list --owner penelope        # 持ち主で絞り込む
vmctl get k8s-cp1
vmctl stop k8s-cp1                 # ゲストにシャットダウンを頼む（待たない）
vmctl stop --force k8s-cp1         # すぐに止める
vmctl start k8s-cp1
vmctl snapshot create k8s-cp1 before-upgrade
vmctl snapshot list k8s-cp1
vmctl snapshot revert k8s-cp1 before-upgrade
vmctl snapshot delete k8s-cp1 before-upgrade
vmctl delete k8s-cp1               # ディスクとスナップショットも消える
```

- 出力は既定で表になる。`-o json` を付けると API の応答をそのまま（整形して）出す。スクリプトではこちらを使う。
- フラグは引数の前にも後にも置ける。`-o`・`--server`・`--token-file` はコマンドの前に置く。
- 失敗すると、API のエラーの `code` と `message` を標準エラーに出して `1` で終わる。コマンドラインの誤りは `2` で終わる。
- `--user-data` は cloud-init の user-data のファイルで、`-` なら標準入力から読む。中身はそのまま VM に渡る（ADR 0005）。

## 開発

```bash
make help        # ターゲットの一覧
make test        # Go のツールチェーンだけで通るテスト
make fmt-check vet lint test tidy vuln   # CI と同じ検査
```

`make test` は libvirt にも外部のコマンドにもネットワークにも触れない。libvirt は `vm.Hypervisor`、`qemu-img` と `xorriso` は `command.Runner`、イメージの取得は `vm.Images` の後ろにあり、テストでは `internal/vm/vmtest` の偽物に差し替える。`internal/libvirt` だけは実機の libvirt が要るので、状態の対応づけとエラーの分類のほかはテストしていない。`internal/server` のテストは `api/openapi.yaml` を読み、経路・フィールド・返す状態コードが契約とずれていないかを確かめる。

## ライセンス

MIT
