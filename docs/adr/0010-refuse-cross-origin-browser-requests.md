# 0010. ブラウザのよそのサイトからの書き込みを拒む

- Date: 2026-10-30
- Status: Accepted

## Context

画面（[[0001]]）は devbox proxy の後ろにあり、ブラウザは GitHub ログインで得た proxy の cookie で認証される。proxy は cookie を確かめると、その利用者の名前を `X-Devbox-User` に入れて vmapi へ渡す（[[0002]]）。vmapi は cookie を見ず、ヘッダだけを読む。

このため、ログインしたままのブラウザで別のサイトを開くと、そのサイトのページがフォームや `fetch` で `https://vm.poissonnerie.dev` へ `POST` を送れる。ブラウザは cookie を付けて送り、proxy はそれを本人の操作として vmapi に渡す。画面のボタン（起動・停止・削除）だけでなく、API の作成や削除も同じである。本文を `text/plain` で送れば、事前のプリフライトなしに JSON の形をした本文を届けることもできる。

一方で、`vmctl`・codemode の道具・`curl` はブラウザではなく、Bearer token で proxy を通る。これらは cookie を持たず、よそのサイトから操られることもない。

対策の候補は、フォームに埋め込む CSRF token、`SameSite` の cookie、リクエストの出どころを示すヘッダの確認である。CSRF token は vmapi がセッションの状態を持つことになり、状態を metadata だけに置く決定（[[0003]]）に沿わない。cookie は proxy のものなので、vmapi からは属性を変えられない。

## Decision

**ブラウザがよそのオリジンから送ったと示す、安全でないメソッドのリクエストは、画面と API の区別なく `403` で拒む。** Go の標準ライブラリの `net/http` の `CrossOriginProtection` を使い、すべての経路の前に置く。

- 判定はブラウザが付けるヘッダで行う。`Sec-Fetch-Site` が `same-origin` か `none` でなければ拒む。`Sec-Fetch-Site` が無く `Origin` があれば、`Origin` のホストがリクエストの `Host` と一致しなければ拒む。
- `GET`・`HEAD`・`OPTIONS` は判定しない。これらで状態を変える経路は作らない。画面のボタンはすべて `POST` にする。
- どちらのヘッダも無いリクエストは、ブラウザ以外からのものとして通す。`vmctl` やスクリプトはそのまま使える。
- 拒んだときの本文は API のエラーの形（[[0007]]）にし、`code` は `permission_denied` とする。

## Consequences

- 画面は vmapi の状態を増やさずに守られる。フォームに token を埋める必要も無い。
- 判定は 2023 年以降の主なブラウザがすべて送る `Sec-Fetch-Site` に頼る。それより古いブラウザでは `Origin` と `Host` の比較になる。proxy が `Host` を書き換えて転送する設定にすると、`Sec-Fetch-Site` を送らない古いブラウザからの画面の操作が拒まれる。今のブラウザには影響しない。
- 同じサイトの別のサブドメイン（`same-site`）からの書き込みも拒む。devbox の他のアプリから vmapi を操作させることは考えない。
- これはブラウザを踏み台にした操作を防ぐものであり、[[0002]] と同じく、名前を名乗れる利用者からの防御ではない。
- 判定は HTTP の層だけで完結するので、`make test` でヘッダを付けたリクエストを送って確かめる。
