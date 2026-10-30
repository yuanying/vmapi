# 0005. cloud-init の seed を vmapi が作る

- Date: 2026-10-30
- Status: Accepted

## Context

VM の初期設定は cloud-init で行う。cloud image は NoCloud データソースとして、ボリューム名 `cidata` の ISO から `user-data`・`meta-data`・`network-config` を読む。

呼ぶ側が決めたいのは、ユーザー、ssh の鍵、パッケージ、起動時に走らせるものなど、ゲストの中身である。ネットワークの設定は vmapi が IP を払い出す（[[0003]]）以上、vmapi が決めないと食い違う。

ISO を作る方法には、Go のライブラリで書き出す方法と、外部のコマンド（`xorriso`・`genisoimage`）を呼ぶ方法がある。cloud-init がファイルを見つけるには、ファイル名が `user-data` のまま読めなければならない。ISO 9660 の基本の形ではファイル名が大文字と 8.3 形式に丸められるので、Rock Ridge か Joliet の拡張が要る。

## Decision

### seed の中身

- **`user-data` は、呼ぶ側が送った本文をそのまま入れる。** vmapi は中を解釈せず、検証もしない。`#cloud-config` に限らず、cloud-init が受け付ける形（シェルスクリプト、MIME multipart など）は何でも使える。省略されたら、中身の無い `#cloud-config` を入れる。
- **`meta-data` は vmapi が作る。** `instance-id` はドメインの UUID、`local-hostname` は VM の名前とする。
- **`network-config` は vmapi が作る。** network config の version 2 で、vmapi がドメインに付けた MAC アドレスに一致するインターフェイスに、払い出した IP・設定の prefix・gateway・DNS を静的に書く。インターフェイス名（`ens3`・`enp1s0` など）はマシンの種類で変わるので、名前ではなく MAC で一致させる。MAC は QEMU の範囲（`52:54:00` で始まる）で vmapi が決める。

呼ぶ側が `user-data` の中でネットワークを書き換えることは止めない。書き換えれば、vmapi が覚えている IP とゲストの実際の IP が食い違うが、それは呼ぶ側の責任とする。

### seed ISO の作り方

**`xorriso -as mkisofs` を外部のコマンドとして呼ぶ。** ボリューム名を `cidata` にし、Joliet と Rock Ridge を付ける。既存のスクリプトが `genisoimage` に渡していた形と同じである。コマンドは vmapi のイメージに入れる。

Go のライブラリで ISO を書く案は採らない。確かめた `github.com/kdomanski/iso9660`（v0.4.0）は、Rock Ridge を読めるが書けず、書き出すときにファイル名を ISO 9660 の基本の形に丸める。cloud-init が読めるファイル名を書けるライブラリを自分で保守するより、広く使われているコマンドを呼ぶほうが確かである。

ISO を使わない案（NoCloud の `ds=nocloud;s=http://...` を SMBIOS で渡し、vmapi が HTTP で seed を配る）も採らない。ゲストから vmapi へ、proxy の認証を経ない経路を開けることになる。

外部のコマンドは、Go のインターフェイスの後ろに置く。`make test` ではそれを偽物に差し替え、渡す引数とファイルの中身だけを確かめる。

## Consequences

- 呼ぶ側は cloud-init の機能をすべて使える。代わりに、vmapi は `user-data` の誤りを教えられない。誤りはゲストの中の cloud-init のログでしか分からない。
- seed ISO は VM の置き場に作り、VM を消すまで残す。cloud-init は `instance-id` が変わらない限り二度目の起動で同じ設定を繰り返さないので、残しておいても害は無い。
- vmapi のイメージは、静的なバイナリだけを入れる distroless ではなく、`xorriso` を入れた Debian になる。
- `xorriso` の呼び出しが実際に cloud-init に読める ISO を作るかは、`make test` では確かめない。実機での確認（VM を作って ssh で入り、`user-data` が効いていることを見る）で確かめる。
