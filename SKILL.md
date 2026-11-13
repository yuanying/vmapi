---
name: vmapi
description: |
  vmapi の CLI `vmctl` で、libvirt の VM を作る・一覧する・見る・起動する・停止する・消す、スナップショットを取る・戻す・消す。
  トリガー: "vmctl", "vmapi", "VM を作って", "VM を消して", "VM の一覧", "スナップショットを取って", "スナップショットに戻して"
  使用場面: (1) 検証用の VM を立てて SSH で入る、(2) VM の状態・IP・持ち主を確かめる、(3) 試す前にスナップショットを取り、失敗したら戻す、(4) 自分の VM の合計（台数・vCPU・メモリ・ディスク）を数える
---

# vmctl で VM を扱う

`vmctl` は vmapi の HTTP API を呼ぶ CLI である。VM は誰からも見えるが、起動・停止・削除とスナップショットの作成・復元・削除ができるのは、その VM の持ち主と管理者だけである。持ち主は、VM を作ったときの token の利用者になる。

ここに書くのは、どの環境でも変わらない `vmctl` の使い方である。宛先の URL、token の置き場所、SSH の鍵、作ってよい台数や大きさの上限は、使う環境の手引きに従う。以下の例の名前（`vmapi.example.com`、`alice`、`demo-cp1`）は架空のものである。

## 入れる

```bash
go install github.com/yuanying/vmapi/cmd/vmctl@v0.2.0   # 版はリリースのタグから選ぶ。@latest でもよい
vmctl --version
```

Go が無い環境では、GitHub のリリースにある `vmctl_<版>_<OS>_<アーキテクチャ>.tar.gz` を展開して `PATH` に置く。

## 宛先と token を渡す

`vmctl` は、宛先の URL と、devbox proxy の Bearer token を要る。**まずは、コマンドの先頭に環境変数を付けて渡す。** その呼び出しだけに効き、手元に何も残らない。

```bash
VMCTL_SERVER=https://vmapi.example.com VMCTL_TOKEN="$TOKEN" vmctl whoami
```

| 何を | 決め方（先にあるものが優先） |
|---|---|
| 宛先 | `--server`、`VMCTL_SERVER`、組み込みの既定値 |
| token | `--token-file` で指したファイル、`VMCTL_TOKEN`、`VMCTL_TOKEN_FILE` で指したファイル、`$XDG_CONFIG_HOME/vmctl/token`（無ければ `~/.config/vmctl/token`） |

- 宛先は毎回明示する。組み込みの既定値は特定の環境のものであり、自分の環境とは限らない。
- token をコマンドラインの引数で渡す方法は無い。プロセスの一覧やシェルの履歴に残るからである。`VMCTL_TOKEN="$TOKEN"` のように、変数やファイルから渡す。
- token は出力にもエラーにも出ない。
- ファイルに置く形（`~/.config/vmctl/token`、`chmod 600`）もある。**ただしファイルは home に残り続け、その token で利用者の VM をすべて操作できる。** 共有の環境や、作業が終われば捨てる環境では使わない。ファイルは持ち主以外が読める権限だと拒まれる。

以降の例では、宛先と token は渡してあるものとして省く。

## 共通の約束

- 出力は既定で表である。**スクリプトや判断に使うときは `-o json` を付ける。** API の応答がそのまま整形されて出る。`-o`・`--server`・`--token-file` はコマンド名の前に置く（`vmctl -o json list`）。
- 終了コードは、成功が `0`、API やネットワークの失敗が `1`、コマンドラインの誤りが `2`。API の失敗のときは標準エラーに `vmctl: <code>: <message>` が出る。
- 主な `code`: `invalid_argument`（値の誤り・範囲外）、`unauthenticated`（token が通らない）、`permission_denied`（持ち主でも管理者でもない。何も変わっていない）、`not_found`、`already_exists`（その名前はホストのどこかで使われている）、`ip_in_use`、`ip_exhausted`、`unavailable`（libvirt に届かない、イメージを取れなかった。時間をおけば通ることがある）。
- VM とスナップショットの名前は DNS のラベルの形（小文字で始まり、小文字・数字・ハイフン、63 文字まで）。VM の名前はホスト全体で一意で、そのままホスト名になる。

## 見る

```bash
vmctl whoami                 # 自分は誰か（USER）と、管理者か（ADMIN）
vmctl images                 # 作るときに選べるイメージ
vmctl list                   # すべての VM。OWNER の列が持ち主
vmctl list --owner alice     # 持ち主で絞る
vmctl get demo-cp1           # 1 台
```

JSON では、`whoami` は `user` と `admin`、`images` は `images[]`（`name`・`description`）、`list` は `vms[]`、`get` は VM 1 台を返す。VM は `name`・`owner`・`image`・`vcpus`・`memory_mib`・`disk_gib`・`ip`・`state`・`created_at` を持つ。`state` は `running`・`shut_off`・`shutting_down`・`paused`・`crashed`・`suspended`・`unknown` のどれかで、値は増えることがある。

```bash
vmctl -o json get demo-cp1 | jq -r .ip
vmctl -o json list | jq -r '.vms[] | select(.state == "running") | .name'
me=$(vmctl -o json whoami | jq -r .user)
vmctl -o json list --owner "$me" | jq -r '.vms[] | [.name, .ip, .state] | @tsv'
```

## 作る

```bash
vmctl create demo-cp1 --image <images の NAME> --vcpus 2 --memory-mib 4096 --disk-gib 40 \
  --user-data cloud-config.yaml
```

- 省いたときは `--vcpus 2`・`--memory-mib 2048`・`--disk-gib 20`。ディスクはイメージより小さくできない。
- IP は空いている最も小さいアドレスが払い出され、応答の `ip` に出る。`--ip` で指すこともできる（範囲の外は `invalid_argument`、使用中は `ip_in_use`）。
- 応答は、作り終えて起動した VM である。`state` が `running` でも、ゲストの中では cloud-init がまだ動いていることがある。

### 作成は終わるまで切らずに待つ

**あるイメージから最初の VM を作るときは、サーバーがイメージを取得し終えるまで応答しない。数分かかることがある。** `vmctl` の側に待ち時間の上限は無い。そのかわり、`vmctl` を途中で止める（Ctrl-C、`SIGTERM`、呼び出し元のタイムアウト）と接続が切れ、**サーバーはイメージの取得ごと作成を取り消す。** やり直すと取得は最初からになる。

- 数十秒で打ち切られる呼び出し方（短いタイムアウトのツール呼び出しなど）で `create` を直接呼ばない。
- 長く動くコマンドとして裏で走らせ、終わりの通知か終了コードを待つ。結果は `-o json` でファイルに受ける。

```bash
vmctl -o json create demo-cp1 --image <イメージ> --user-data cloud-config.yaml \
  > demo-cp1.json 2> demo-cp1.err
echo "exit=$?"
```

- 終わったら、終了コードが `0` なら `demo-cp1.json` に VM がある。`1` なら `demo-cp1.err` の `code` を見る。
- 待っているあいだに同じ `create` をもう一度送らない。名前の重複はイメージの取得が済んでから判定されるので、すぐには拒まれず、後から片方が `already_exists` で終わるだけである。
- 失敗した後や、待ち切れずに止めてしまった後は、`vmctl get demo-cp1` で VM が残っていないかを確かめてからやり直す。

### cloud-init で利用者と公開鍵を入れる

`--user-data` に cloud-init の user-data を渡す（`-` なら標準入力）。中身は確かめられずにそのまま VM に渡る。ホスト名とネットワークはサーバーが書くので、user-data に書かない。大きさは 1 MiB まで。

```yaml
#cloud-config
users:
  - name: alice
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - ssh-ed25519 AAAA...example alice@example
```

```bash
vmctl create demo-cp1 --image <イメージ> --user-data - < cloud-config.yaml
ssh alice@"$(vmctl -o json get demo-cp1 | jq -r .ip)"
```

SSH が通るのは、ゲストが起動して cloud-init が鍵を入れ終えた後である。作成の直後につながらなければ、少し間をおいて試す。

## 起動・停止

```bash
vmctl start demo-cp1
vmctl stop demo-cp1          # ゲストにシャットダウンを頼むだけで、止まるのを待たない
vmctl stop --force demo-cp1  # すぐに電源を切る
```

`stop` は止まるのを待たない。止まったことを確かめるには `state` が `shut_off` になるまで `get` を見る。決めた時間で止まらなければ `--force` を使う。

```bash
for i in $(seq 60); do
  [ "$(vmctl -o json get demo-cp1 | jq -r .state)" = shut_off ] && break
  sleep 5
done
```

すでに止まっている VM への `stop` も成功する。

## スナップショット

```bash
vmctl snapshot create demo-cp1 before-upgrade
vmctl snapshot list demo-cp1
vmctl snapshot revert demo-cp1 before-upgrade
vmctl snapshot delete demo-cp1 before-upgrade
```

- 動いている VM のスナップショットはメモリを含み、取るあいだ VM が一時停止する。メモリが大きいほど長い。
- 戻すと、取ったときの状態（動いていたなら動いている、止まっていたなら止まっている）に戻る。**取った後に書いた内容は失われる。** `list` の `VM STATE`（JSON では `vm_state`）が、戻した後の状態である。
- `snapshot delete` は成功しても何も出力しない。

## 消す

```bash
vmctl delete demo-cp1
```

- **動いていても確かめずに電源を切って消す。** ディスクとスナップショットもすべて消え、取り消せない。消す前に `vmctl get` で名前と持ち主を確かめる。
- 成功しても何も出力しない。終了コードで判断する。
- 消した VM の IP は、次に作る VM に払い出されうる。**消した後は、手元の `known_hosts` からその IP と名前を消す。** 残すと、同じ IP の新しい VM に SSH するときに鍵の不一致で止まる。

```bash
ssh-keygen -R 192.0.2.10   # 消した VM の IP
ssh-keygen -R demo-cp1     # 名前で入っていたなら名前も
```

IP は消す前に `vmctl -o json get demo-cp1 | jq -r .ip` で控えておく。

## 合計を数える

自分の VM の台数・vCPU・メモリ・ディスクの合計は、`list` の JSON を `jq` で足す。上限と比べる前に、止まっている VM を数えに入れるかを、使う環境の決まりで確かめる。ディスクは止まっていても場所を取る。

```bash
me=$(vmctl -o json whoami | jq -r .user)
vmctl -o json list --owner "$me" | jq '.vms | {
  count: length,
  vcpus: (map(.vcpus) | add // 0),
  memory_mib: (map(.memory_mib) | add // 0),
  disk_gib: (map(.disk_gib) | add // 0),
  running: (map(select(.state == "running")) | {
    count: length,
    vcpus: (map(.vcpus) | add // 0),
    memory_mib: (map(.memory_mib) | add // 0)
  })
}'
```

`--owner` を外せば、ホスト全体の合計になる。
