# ADR

vmapi の設計の決定。実装はここに書かれた決定に従う。覆すときは、先に ADR を足すか改訂する。

| 番号 | 決定 |
|---|---|
| [0001](0001-a-thin-per-vm-layer-over-libvirt.md) | libvirt の上に VM 単位の薄い層を置く。クラスタは呼ぶ側、既存の VM は扱わない、作成は同期 |
| [0002](0002-separate-vms-by-owner.md) | `X-Devbox-User` を持ち主として VM を分ける。管理者は全部。事故を防ぐ線引きであり悪意への防御ではない |
| [0003](0003-keep-state-only-in-libvirt-domain-metadata.md) | 状態はドメイン metadata だけ。名前は全体で一意、IP の空きは metadata から導きプロセス内のロックで守る |
| [0004](0004-os-images-from-a-server-side-list.md) | OS イメージは設定の一覧（名前・URL・SHA-256）から。初回に取得して保管する |
| [0005](0005-cloud-init-seed.md) | user-data は本文のまま、meta-data と network-config は vmapi が作る。seed ISO は `xorriso` で作る |
| [0006](0006-disks-and-snapshots.md) | ディスクは `qemu-img` で作る qcow2 の overlay、スナップショットは内部スナップショット |
| [0007](0007-http-api-contract.md) | API の契約は `api/openapi.yaml`。JSON、追加で壊れない形、エラーの形は一つ |
| [0008](0008-run-in-a-devbox-apps-container.md) | devbox の apps のコンテナで動かす。libvirt のソケットを持つコンテナはホストの root 相当 |
| [0009](0009-configuration-file.md) | 設定は `~/.config/vmapi/config.yaml`。リポジトリにも dotfiles にも置かない |
| [0010](0010-refuse-cross-origin-browser-requests.md) | ブラウザがよそのオリジンから送った `POST` などは画面・API ともに `403`。`Sec-Fetch-Site` と `Origin` で判定する |
