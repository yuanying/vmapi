# vmapi

libvirt の上に置く、VM 単位の薄い HTTP API。VM の作成・一覧・起動・停止・削除と
スナップショットを、持ち主ごとに分けて扱う。

- `vmapi` — API サーバー。devbox の apps のコンテナで動き、devbox proxy の後ろに置く
- `vmctl` — 人間のシェルとスクリプト向けの CLI

API の契約は `api/openapi.yaml`、設計の決定は `docs/adr/`、設定ファイルは
`docs/config.md` にある。

## 開発

```bash
make help        # ターゲットの一覧
make test        # Go のツールチェーンだけで通るテスト
make fmt-check vet lint test tidy vuln   # CI と同じ検査
```

## ライセンス

MIT
