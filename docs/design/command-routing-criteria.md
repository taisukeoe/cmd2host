# コマンドルーティング設計基準

## 設計原則

cmd2hostは「ホスト認証が必要なCLIツール」に特化し、シンプルさを保つ。

## ホストで実行すべきコマンド

### 判定基準
1. **ホストの認証情報が必須** - OAuth、SSH鍵、APIトークンなど
2. **ホストのデーモンへのアクセスが必要** - Docker daemon等
3. **セキュリティ境界を越える必要がある** - コンテナからホストリソースへ

### 対象コマンド
- `gh` (GitHub CLI) - OAuth認証
- `glab` (GitLab CLI) - OAuth認証
- `docker` - Docker Desktop/daemon
- `aws`, `gcloud`, `az` - クラウドCLI
- `git push/fetch/pull` - SSH鍵使用時

### 実装方針
**明示的オプトイン**: `devcontainer.json`で明示的に指定されたコマンドのみ

```json
{
  "features": {
    "ghcr.io/taisukeoe/cmd2host/cmd2host:1": {
      "commands": "gh,docker"  // 明示的に列挙
    }
  }
}
```

## コンテナ内で実行すべきコマンド

### 判定基準
1. **開発環境の一貫性が重要** - チーム全体で同じバージョン・設定を使う
2. **コンテナ内のファイルシステムを操作** - ソースコード、ビルド成果物
3. **高頻度実行** - パフォーマンス重要、ネットワーク越しは非効率

### 対象コマンド
- **パッケージマネージャー**: `npm`, `pip`, `cargo`, `go`, `mvn`
- **開発ツール**: linter, formatter, compiler, test runner
- **Git読み取り操作**: `status`, `log`, `diff`, `show`
- **Git書き込み操作**: `add`, `commit` (ローカルのみ)
- **シェルユーティリティ**: `ls`, `cat`, `grep`, `find`

### 実装方針
**デフォルトでコンテナ内**: cmd2hostは何もしない（通常のPATH解決）

## 判定フロー

```
コマンド実行
  ↓
cmd2host wrapperが存在？
  ├─ NO → コンテナ内で実行（通常のPATH）
  └─ YES → Layer 1: コマンド名チェック
           ↓
           commands設定に含まれる？
           ├─ NO → コンテナ内にfallback
           └─ YES → Layer 2: Operation検証
                    ↓
                    Operationが許可されている？
                    ├─ NO → エラー（denied_reason返却）
                    └─ YES → ホストで実行
```

## 割り切り（スコープ外）

### 1. 自動判定はしない
**理由**:
- 誤判定のリスク（セキュリティ）
- 複雑性の増加
- ユーザーの意図が不明確

**代わりに**: 明示的な設定を要求

### 2. 動的な切り替えはしない
**理由**:
- `git commit` vs `git push`の違いなどコマンド内で判断が複雑
- エラーメッセージが分かりにくくなる

**代わりに**: Operation定義で明示的に分離
- `git_commit` (コンテナ内 or 制限付きホスト)
- `git_push` (ホストのみ)

### 3. すべてのコマンドはサポートしない
**理由**:
- 用途が明確なものに限定
- メンテナンスコスト削減

**代わりに**: 認証が必要なCLIツールに特化

## デフォルト設定推奨

### 最小構成（リードオンリー）
```json
{
  "commands": "gh",
  "profile": "gh_readonly"  // PR/Issue閲覧のみ
}
```

### 拡張構成（AI agent向け）
```json
{
  "commands": "gh,git",
  "profile": "ai_developer"  // 制限付きwrite権限
}
```

### フル構成（信頼されたユーザー）
```json
{
  "commands": "gh,docker,aws",
  "profile": "full_access"
}
```

## 実装の簡素化

### 現在の実装で十分
1. **Layer 1**: wrapper scripts (`cmd-wrapper.sh`) - コマンド名で判定
2. **Layer 2**: Operation validation (`operations.go`) - 詳細検証
3. **Profile**: Repository/branch制限 (`profile.go`)

### 追加不要なもの
- ❌ コマンドの自動分類
- ❌ ヒューリスティック判定
- ❌ 機械学習ベースの判定
- ❌ 動的なfallback機構

### 改善すべき点
- ✅ デフォルトOperationセットの充実
- ✅ Profile templateの提供
- ✅ エラーメッセージの改善（"なぜ拒否されたか"）
- ✅ ドキュメント（どのコマンドをproxyすべきか）

## まとめ

**シンプルな原則:**
> ホスト認証が必要なら cmd2host、それ以外はコンテナ内

**実装方針:**
> 明示的設定のみサポート、自動判定はしない

**メリット:**
- 予測可能な動作
- セキュリティリスク低減
- メンテナンス容易
- ユーザーの理解が容易
