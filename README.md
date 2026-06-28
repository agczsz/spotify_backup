# Spotify 歌单备份与恢复工具 (Go 版)

这是一个使用 Go 语言开发的轻量级、无 GUI 命令行工具，旨在帮助您对 Spotify 歌单进行高保真备份，并在同一账号（修改名称测试）或另一个 Spotify 账户上按完全一致的顺序进行恢复。

## 🌟 核心特性

- **安全性优先 (OAuth 2.0 PKCE)**: 默认使用 PKCE 流程，本地运行时完全不暴露、不存储任何 Client Secret，保护账户安全。
- **两套运行模式**:
  - **本地交互模式**: 启动临时 Web 服务（监听 `http://127.0.0.1:8080/callback`），自动拉起浏览器完成首次授权并保存凭证。
  - **无交互 CI 模式 (GitHub Actions)**: 完全支持通过环境变量（`SPOTIFY_CLIENT_ID`、`SPOTIFY_REFRESH_TOKEN`）运行，在 Token 过期时自动调用 API 刷新。
- **完全按序恢复**: 完美保留歌单中的歌曲原始排序。
- **高保真元数据**: 支持导出为 `.json` 或 `.yaml` 文件，包含歌单元信息及歌曲名称、歌手、专辑、时长、URI 等。
- **本地曲目跳过**: 自动识别并跳过由于 Spotify 限制而无法通过 Web API 导入的本地音乐文件（`spotify:local:`），并在控制台提供警告。
- **防冲突重命名**: 导入时支持添加前缀 (`-prefix`) 或后缀 (`-suffix`)，便于在同一个账户下进行恢复测试，防止重名混乱。
- **已点赞歌曲备份支持**: 特别集成了对用户个人“已点赞的歌曲” (Liked Songs) 的备份与还原。在备份时，将其作为虚拟歌单导出；导入时使用专属 API 批量保存回目标账户的“已点赞歌曲”媒体库中。
- **速率限制保护 (HTTP 429)**: 精准拦截 HTTP 429 速率限制响应，自动读取 `Retry-After` 头部，延时缓冲后重试。
- **指数退避重试**: 针对 Spotify 5xx 服务端临时波动错误提供指数退避重试，保障大规模歌单备份的稳定性。

---

## 🛠️ 第一步：创建 Spotify 开发者应用

要使用 Spotify API，您需要注册一个 Spotify 开发者应用：

1. 登录 [Spotify Developer Dashboard](https://developer.spotify.com/dashboard)。
2. 点击 **Create App**。
3. 填写基本信息：
   - **App name**: 例如 `Spotify Backup Tool`
   - **App description**: 歌单备份工具
   - **Redirect URIs**: 必须填写 **`http://127.0.0.1:8080/callback`** （⚠️ 注意：请使用 `127.0.0.1`，不要使用 `localhost`）。
4. 勾选同意条款并保存。
5. 在应用管理页面中，复制您的 **Client ID**。

---

## 💻 第二步：本地安装与运行

确保您的电脑已安装 Go 环境 (Go 1.18+)，克隆本项目到本地后，在项目根目录下执行：

### 1. 编译项目
```bash
go build -o spotify-backup
```

### 2. 交互式登录
运行以下命令以获取您的 Token：
```bash
# 请将 YOUR_CLIENT_ID 替换为您在开发者后台复制的 Client ID
./spotify-backup login -client-id YOUR_CLIENT_ID
```
程序会：
1. 在控制台输出授权链接。
2. 自动为您打开系统默认浏览器。
3. 授权成功后，本地临时服务器接收到授权码，与 Spotify 交换 Token。
4. 在本地生成 **`credentials.json`** 配置文件。
5. **在控制台输出 GitHub Secrets 环境变量格式**，方便您复制到 GitHub 仓库。

### 3. 导出歌单备份
```bash
# 默认导出为 JSON 格式
./spotify-backup export -file backup.json

# 导出为 YAML 格式
./spotify-backup export -file backup.yaml

# 仅备份指定的部分歌单 (用逗号分隔歌单 ID)
./spotify-backup export -file target.yaml -playlist-ids 37i9dQZF1DXcBWIGmqecem,4g4y9...
```

### 4. 导入恢复歌单
由于只有 Premium 账户才能使用 API，如果需要在同账户下测试导入效果，建议使用 **`-suffix`** 后缀参数对歌单进行重命名，以防与已有歌单冲突：
```bash
# 从备份文件恢复，并为所有创建的歌单名称添加 "_测试备份" 后缀
./spotify-backup import -file backup.yaml -suffix _测试备份

# 或者指定前缀
./spotify-backup import -file backup.yaml -prefix 恢复_
```

---

## 🤖 第三步：在 GitHub Actions 中配置每日定时自动备份

通过在 GitHub 中配置，您可以实现无需本地运行、每日定时自动将您的歌单备份并提交保存到私有 GitHub 仓库中。

### 1. 配置仓库 Secrets
在您的 GitHub 仓库页面，依次点击 **Settings** -> **Secrets and variables** -> **Actions** -> **New repository secret**，添加以下两个 Secret：

| Secret 名称 | 说明 | 如何获取 |
| :--- | :--- | :--- |
| `SPOTIFY_CLIENT_ID` | 您的 Spotify 开发者 Client ID | 开发者后台控制面板 |
| `SPOTIFY_REFRESH_TOKEN` | 您的 OAuth 刷新 Token | 本地执行 `./spotify-backup login` 后控制台输出的 `SPOTIFY_REFRESH_TOKEN` 的值 |

> [!IMPORTANT]
> 请注意保护您的 `SPOTIFY_REFRESH_TOKEN`，切勿将其硬编码提交到公开仓库中！

### 2. 创建 GitHub Action 工作流
在项目根目录下创建文件夹及文件：`.github/workflows/spotify-backup-cron.yml`，内容参考如下：

```yaml
name: Spotify Playlists Daily Backup

on:
  schedule:
    # 每天 UTC 时间 20:00 (北京时间凌晨 4:00) 自动运行
    - cron: '0 20 * * *'
  workflow_dispatch: # 支持手动触发运行

jobs:
  backup:
    runs-on: ubuntu-latest
    permissions:
      contents: write # 需要写入权限以将备份文件提交回仓库

    steps:
      - name: Checkout Code
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.22'

      - name: Run Backup Tool
        env:
          SPOTIFY_CLIENT_ID: ${{ secrets.SPOTIFY_CLIENT_ID }}
          SPOTIFY_REFRESH_TOKEN: ${{ secrets.SPOTIFY_REFRESH_TOKEN }}
        run: |
          # 编译程序
          go build -o spotify-backup
          # 导出为 YAML 备份 (会自动在 token 过期时利用 refresh token 刷新)
          ./spotify-backup export -file backups/my_spotify_playlists.yaml

      - name: Commit and Push Changes
        run: |
          git config --global user.name "github-actions[bot]"
          git config --global user.email "github-actions[bot]@users.noreply.github.com"
          git add backups/my_spotify_playlists.yaml
          # 只有在内容有变化时才提交，防止 Action 报错
          git diff --quiet && git diff --staged --quiet || (git commit -m "Auto-backup Spotify playlists: $(date +'%Y-%m-%d %H:%M:%S')" && git push)
```

### 3. 企业微信 Webhook 通知
本工具内置了企业微信机器人通知。当备份成功、失败、或者 Token 失效需要重新登录时，会向默认的 Webhook 发送 Markdown 消息通知备份状态。
- 默认 Webhook: `https://wecom-webhook-relay.ldm1162845582.workers.dev/hook/wjkhneulcojyrqef`

如果您需要更换为自己的 Webhook 或是想禁用通知，可以通过设置环境变量 **`WECOM_WEBHOOK`** 来覆盖它：
- **更换 Webhook**：配置环境变量 `WECOM_WEBHOOK="你的企业微信 Webhook 地址"`
- **禁用 Webhook**：配置环境变量 `WECOM_WEBHOOK="disabled"`（或 `none`）

在 GitHub Actions 中，您可以同样在 GitHub Secrets 中添加 `WECOM_WEBHOOK`（可选）并在任务步骤中注入：
```yaml
      - name: Run Backup Tool
        env:
          SPOTIFY_CLIENT_ID: ${{ secrets.SPOTIFY_CLIENT_ID }}
          SPOTIFY_REFRESH_TOKEN: ${{ secrets.SPOTIFY_REFRESH_TOKEN }}
          WECOM_WEBHOOK: ${{ secrets.WECOM_WEBHOOK }} # 可选，自定义机器人链接
```

---

## 📂 备份文件示例 (.yaml)

```yaml
backup_date: 2026-06-28T12:00:00+08:00
playlists:
  - id: 37i9dQZF1DXcBWIGmqecem
    name: 流行乐精选
    description: 华语流行乐坛热门金曲。
    public: true
    collaborative: false
    tracks:
      - uri: spotify:track:4PTG3Z6ehGkBF3zIqYQG5g
        name: Respect
        type: track
        duration_ms: 147800
        album: I Never Loved a Man the Way I Love You
        artists:
          - Aretha Franklin
      - uri: spotify:track:35R5mF5xXpxD2tH...
        name: ...
```

## ⚖️ 免责声明 (Developer Terms of Service)
本工具仅供个人作为账户数据备份、恢复及迁移目的使用。本工具符合《Spotify 开发者服务条款》，请勿使用本工具导出的数据进行任何机器学习模型训练或长期数据缓存商业行为。
