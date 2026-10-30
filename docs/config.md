# 設定ファイル

vmapi の設定は YAML のファイル一つで与える。置き場は `~/.config/vmapi/config.yaml` で、このリポジトリにも dotfiles にも置かない（ADR 0009）。

- `vmapi --config <パス>` で読むファイルを指定する。省くと `$XDG_CONFIG_HOME/vmapi/config.yaml`、`XDG_CONFIG_HOME` が無ければ `~/.config/vmapi/config.yaml` を読む。
- 知らない項目があれば起動しない。見つかった誤りは、項目の名前を添えてすべて報告する。
- 設定は起動のときに一度だけ読む。変えたら再起動する。

## 項目

「必須」の項目に既定値は無い。無ければ起動しない。

| 項目 | 型 | 既定値 | 内容 |
|---|---|---|---|
| `listen` | 文字列 | `:8080` | HTTP を待ち受けるアドレス |
| `libvirt.uri` | 文字列 | `qemu:///system` | 接続する libvirt の URI |
| `libvirt.socket` | 文字列 | `/var/run/libvirt/libvirt-sock` | libvirt の Unix ソケットのパス（コンテナの中のパス） |
| `storage.dir` | 文字列 | 必須 | ディスクの置き場の絶対パス。ホストと同じパスでマウントする（ADR 0006・0008） |
| `network.bridge` | 文字列 | 必須 | VM をつなぐホストのブリッジ |
| `network.range.start` | IPv4 | 必須 | 払い出す範囲の最初の IP |
| `network.range.end` | IPv4 | 必須 | 払い出す範囲の最後の IP（これも払い出す） |
| `network.prefix` | 整数 | 必須 | VM に設定するプレフィックス長 |
| `network.gateway` | IPv4 | 必須 | VM に設定する gateway |
| `network.dns` | IPv4 の配列 | 必須 | VM に設定する DNS サーバー。一つ以上 |
| `admins` | 文字列の配列 | 空 | 他人の VM も操作できる利用者（`X-Devbox-User` の値）。見ることは誰でもできる。空なら管理者はいない |
| `images` | 配列 | 必須 | 選べる OS イメージ。一つ以上。各要素は下の表 |

`images` の各要素:

| 項目 | 型 | 既定値 | 内容 |
|---|---|---|---|
| `name` | 文字列 | 必須 | 呼ぶ側が選ぶ名前。VM の名前と同じ形（英小文字で始まり、英小文字・数字・ハイフン、63 文字以下）。重複は不可 |
| `url` | 文字列 | 必須 | cloud image の `https://` の URL。中身が変わらない URL を使う |
| `sha256` | 文字列 | 必須 | イメージの SHA-256（16 進 64 文字） |
| `description` | 文字列 | 空 | イメージの一覧に出す説明 |

## 検証の規則

- `network.range.start` は `network.range.end` 以下である。
- 範囲の両端と `network.gateway` は、`network.gateway` と `network.prefix` が決めるサブネットに入っている。
- `network.gateway` と `network.dns` は範囲に入っていない。
- `storage.dir` は絶対パスである。

## boucherie の値

boucherie で使う値（2026-10-30 にユーザーが承認したもの）。払い出す範囲は、既存の VM（porkadot-dev の envs の 192.168.201.x・192.168.202.x、今の dev ホストの 192.168.22.x）や LAN の機器と重ならないように取っている。

| 項目 | 値 |
|---|---|
| `libvirt.uri` | `qemu:///system`（既定値） |
| `libvirt.socket` | `/var/run/libvirt/libvirt-sock`（既定値） |
| `storage.dir` | `/mnt/data/vmapi` |
| `network.bridge` | `br0` |
| `network.range` | `192.168.40.10` 〜 `192.168.40.250` |
| `network.prefix` | `16` |
| `network.gateway` | `192.168.0.1` |
| `network.dns` | `192.168.0.1` |
| `admins` | `yuanying` |
| `images` | `noble`（Ubuntu 24.04）、`jammy`（Ubuntu 22.04） |

これを設定ファイルにすると次のようになる。

```yaml
storage:
  dir: /mnt/data/vmapi
network:
  bridge: br0
  range:
    start: 192.168.40.10
    end: 192.168.40.250
  prefix: 16
  gateway: 192.168.0.1
  dns:
    - 192.168.0.1
admins:
  - yuanying
images:
  - name: noble
    description: Ubuntu 24.04 LTS
    url: https://cloud-images.ubuntu.com/releases/noble/release-20261017/ubuntu-24.04-server-cloudimg-amd64.img
    sha256: 04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94
  - name: jammy
    description: Ubuntu 22.04 LTS
    url: https://cloud-images.ubuntu.com/releases/jammy/release-20261020/ubuntu-22.04-server-cloudimg-amd64.img
    sha256: 2099e63da0b822ef8e05e3d596c1b402eaaae658fc08ab919cb8495bfcdc488b
```

## Ubuntu の cloud image の URL とチェックサムの取り方

1. `https://cloud-images.ubuntu.com/releases/<コード名>/` を開き、日付の付いたディレクトリ（`release-YYYYMMDD/`）を選ぶ。`current/` は中身が変わるので使わない（ADR 0004）。
2. そのディレクトリの `ubuntu-<版>-server-cloudimg-amd64.img` を `url` にする。
3. 同じディレクトリの `SHA256SUMS` から、そのファイル名の行の値を `sha256` にする。

上の例の値は、2026-10-30 の時点の最新のリリース（noble は `release-20261017`、jammy は `release-20261020`）の `SHA256SUMS` から取った。

イメージを新しいリリースに替えるときは、`url` と `sha256` を書き換えて再起動する。古いイメージから作った VM はそのまま動く（ADR 0004）。
