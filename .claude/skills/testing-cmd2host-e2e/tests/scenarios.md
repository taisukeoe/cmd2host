# Evaluation Scenarios for testing-cmd2host-e2e

## Scenario: Run E2E Tests

**Difficulty:** Easy

**Query:** cmd2hostのe2eテストを実行して

**Expected behaviors:**

1. Execute e2e test script
   - **Minimum:** `just test-e2e` または `./test/e2e/run_e2e.sh` を実行
   - **Quality criteria:** 結果を報告、失敗時はトラブルシューティングを提案
   - **Haiku pitfall:** スクリプトを使わず手動でステップ実行
   - **Weight:** 5

---

## Scenario: Quick E2E Test

**Difficulty:** Easy

**Query:** devcontainerは起動済み、e2eテストだけ実行して

**Expected behaviors:**

1. Execute quick test
   - **Minimum:** `just test-e2e-quick` または `--skip-devcontainer` オプション使用
   - **Quality criteria:** オプションの意味を説明
   - **Haiku pitfall:** フルテストを実行してしまう
   - **Weight:** 5

---

## Scenario: Troubleshooting - Token Profile Error

**Difficulty:** Medium

**Query:** e2eテストで"Token does not have a profile assigned"エラーが出た

**Expected behaviors:**

1. Explain the error cause
   - **Minimum:** profileとdefault_profileの関係を説明
   - **Quality criteria:** config.jsonの具体例を提示
   - **Haiku pitfall:** 原因を特定できない
   - **Weight:** 5

2. Provide fix commands
   - **Minimum:** config.json修正とdaemon再起動の手順
   - **Quality criteria:** launchctlコマンドを正確に提示
   - **Haiku pitfall:** 再起動手順を忘れる
   - **Weight:** 4

---

## Scenario: Troubleshooting - Keychain Locked

**Difficulty:** Easy

**Query:** e2eテストでkeychain errorが出た

**Expected behaviors:**

1. Provide unlock command
   - **Minimum:** `security -v unlock-keychain` コマンドを提示
   - **Quality criteria:** フルパス `~/Library/Keychains/login.keychain-db` を含む
   - **Haiku pitfall:** 間違ったコマンドを提示
   - **Weight:** 5

---

## Scenario: Troubleshooting - Daemon Not Running

**Difficulty:** Easy

**Query:** e2eテストでconnection refusedエラーが出た

**Expected behaviors:**

1. Check daemon status
   - **Minimum:** `lsof -i :9876` で確認
   - **Quality criteria:** 結果の解釈も説明
   - **Haiku pitfall:** 確認をスキップ
   - **Weight:** 3

2. Start daemon
   - **Minimum:** launchctl loadコマンドを提示
   - **Quality criteria:** plistのフルパスを含む
   - **Haiku pitfall:** 間違ったパスを提示
   - **Weight:** 5

---

## Scenario: Manual Testing

**Difficulty:** Medium

**Query:** スクリプトを使わずに手動でMCPオペレーションをテストしたい

**Expected behaviors:**

1. Provide manual test command
   - **Minimum:** devcontainer exec + nc コマンドを提示
   - **Quality criteria:** TOKEN取得、JSON形式、タイムアウト設定を含む
   - **Haiku pitfall:** JSON形式を間違える
   - **Weight:** 5

2. Explain expected output
   - **Minimum:** 成功時のレスポンス形式を説明
   - **Quality criteria:** exit_code: 0 の確認方法を説明
   - **Haiku pitfall:** 結果の解釈を省略
   - **Weight:** 3
