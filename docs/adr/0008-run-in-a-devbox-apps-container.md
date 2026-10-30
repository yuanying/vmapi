# 0008. devbox の apps のコンテナで動かす

- Date: 2026-10-30
- Status: Accepted

## Context

vmapi は boucherie の `qemu:///system` を操作し、devbox proxy の後ろで `https://vm.poissonnerie.dev` として公開する。boucherie では、常に動かしておくウェブアプリを devbox の apps として、`restart: always` のコンテナで動かしている（dotfiles の ADR 0011・0012、`devbox/apps/compose.boucherie.yaml`）。proxy は apps にコンテナ名で転送する。

vmapi が libvirt を操作するには、ホストの libvirt のソケット（`/var/run/libvirt/libvirt-sock`）に届く必要がある。**libvirt のソケットに書き込めるものは、ホストの root と同じことができる。** 任意のホストのファイルをディスクにしたドメインを定義し、起動すれば、そのファイルを読み書きできるからである。

## Decision

**vmapi は devbox の apps のコンテナとして動かす。ホストに直接は入れない。** compose の定義と proxy への登録は dotfiles に置き、このリポジトリはイメージ（`ghcr.io/yuanying/vmapi`）だけを出す。

- **libvirt のソケットをマウントする。** コンテナは、ソケットに書き込めるグループ（ホストの `libvirt` グループ）を追加のグループとして持つ。
- **ディスクの置き場（`/mnt/data/vmapi`）を、ホストと同じパスでマウントする。** ドメイン定義と overlay の backing file にはホストのパスが入り（[[0006]]）、それを開くのはホストの libvirt だからである。
- **設定ファイル（[[0009]]）を読み出し専用でマウントし、そのパスを `--config` で渡す。**
- **ホストのポートは公開しない。** apps のネットワーク（`v6net`）にだけつなぎ、proxy がコンテナ名で転送する。proxy の設定は `auth: required` にする。
- **プロセスは一つだけ動かす**（[[0003]]）。

### root 相当であることへの手当て

vmapi のコンテナはホストの root 相当の権限を持つ。vmapi は次を守る。

- 呼ぶ側から、ドメインの XML、ファイルのパス、イメージの URL を受け取らない。ドメイン定義は vmapi が自分で組み立て、パスは設定のディスクの置き場の下に限る（[[0004]]・[[0006]]）。
- 名前は DNS のラベルの形に限るので（[[0003]]）、パスの区切りや `..` を含まない。
- metadata の無いドメインには触らない（[[0003]]）。

これらは呼ぶ側の誤りが VM の外に及ばないようにするためのもので、[[0002]] と同じく、悪意を持った利用者からホストを守るものではない。vmapi に管理者として届くものは、事実上ホストの root である。

## Consequences

- vmapi は devbox の再起動やホストの再起動の後に、自分で立ち上がる。
- コンテナを動かす利用者とグループ（ファイルの持ち主、`libvirt` グループの GID）はホストに合わせて dotfiles の compose で決める。イメージの既定の利用者は root でない UID（65532）で、compose が上書きする。
- libvirt は VM の起動のときにディスクのファイルの持ち主を QEMU の利用者に変える（dynamic ownership）。vmapi がファイルを消すにはディレクトリへの書き込み権限があればよいが、実機で確かめる。
- apps のネットワークに居るほかのコンテナは、proxy を経ずに vmapi に届き、`X-Devbox-User` を名乗れる（[[0002]] の前提）。そこに置くコンテナを増やすときは、それが vmapi の管理者を名乗れることを念頭に置く。
