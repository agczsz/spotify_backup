package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"spotify-backup/backup"
	"spotify-backup/config"
	"spotify-backup/spotify"
)

func main() {
	// 定义子命令
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	subcommand := os.Args[1]

	switch subcommand {
	case "login":
		loginCmd := flag.NewFlagSet("login", flag.ExitOnError)
		clientIDFlag := loginCmd.String("client-id", "", "Spotify Application Client ID")
		configFlag := loginCmd.String("config", "credentials.json", "配置文件保存路径")

		_ = loginCmd.Parse(os.Args[2:])

		clientID := *clientIDFlag
		if clientID == "" {
			// 尝试从环境变量获取
			clientID = os.Getenv("SPOTIFY_CLIENT_ID")
		}

		if clientID == "" {
			// 如果命令行和环境都没有，则交互式询问
			fmt.Print("请输入您的 Spotify Client ID (可在 Spotify 开发者控制台获取): ")
			reader := bufio.NewReader(os.Stdin)
			input, err := reader.ReadString('\n')
			if err != nil {
				fmt.Printf("读取输入失败: %v\n", err)
				os.Exit(1)
			}
			clientID = strings.TrimSpace(input)
		}

		if clientID == "" {
			fmt.Println("❌ 错误: Client ID 不能为空。")
			os.Exit(1)
		}

		fmt.Println("🚀 开始初始化 Spotify OAuth PKCE 登录流程...")
		cfg, err := spotify.InteractiveLogin(clientID)
		if err != nil {
			fmt.Printf("❌ 登录失败: %v\n", err)
			os.Exit(1)
		}

		// 保存本地配置
		if err := config.SaveConfig(*configFlag, cfg); err != nil {
			fmt.Printf("❌ 保存配置文件失败: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("✅ 登录成功！配置已保存至: %s\n", *configFlag)

		// 打印 GitHub Action 环境变量格式，方便用户直接复制到 GitHub Secrets
		cfg.PrintGitHubEnv()

	case "export":
		exportCmd := flag.NewFlagSet("export", flag.ExitOnError)
		fileFlag := exportCmd.String("file", "backup.json", "备份文件输出路径 (支持 .json, .yaml, .yml)")
		configFlag := exportCmd.String("config", "credentials.json", "配置文件路径")
		playlistsFlag := exportCmd.String("playlist-ids", "", "指定要导出的歌单 ID，用逗号分隔 (默认全部)")

		_ = exportCmd.Parse(os.Args[2:])

		cfg, err := config.LoadConfig(*configFlag)
		if err != nil {
			fmt.Printf("❌ 加载配置失败: %v\n", err)
			os.Exit(1)
		}

		if cfg.ClientID == "" || (cfg.AccessToken == "" && cfg.RefreshToken == "") {
			fmt.Println("❌ 错误: 未检测到有效的登录凭证。")
			fmt.Println("  - 本地运行: 请先执行 `./spotify-backup login` 命令。")
			fmt.Println("  - CI (GitHub Actions) 运行: 请确保已正确配置 SPOTIFY_CLIENT_ID 和 SPOTIFY_REFRESH_TOKEN 环境变量。")
			os.Exit(1)
		}

		// 解析指定的歌单 ID
		var playlistIDs []string
		if *playlistsFlag != "" {
			parts := strings.Split(*playlistsFlag, ",")
			for _, p := range parts {
				trimmed := strings.TrimSpace(p)
				if trimmed != "" {
					playlistIDs = append(playlistIDs, trimmed)
				}
			}
		}

		client := spotify.NewClient(cfg, *configFlag)
		if err := backup.ExportPlaylists(client, *fileFlag, playlistIDs); err != nil {
			fmt.Printf("❌ 备份失败: %v\n", err)
			os.Exit(1)
		}

	case "import":
		importCmd := flag.NewFlagSet("import", flag.ExitOnError)
		fileFlag := importCmd.String("file", "backup.json", "备份文件路径 (支持 .json, .yaml, .yml)")
		configFlag := importCmd.String("config", "credentials.json", "配置文件路径")
		prefixFlag := importCmd.String("prefix", "", "导入时歌单名称的前缀")
		suffixFlag := importCmd.String("suffix", "", "导入时歌单名称的后缀 (例如: _测试)")

		_ = importCmd.Parse(os.Args[2:])

		cfg, err := config.LoadConfig(*configFlag)
		if err != nil {
			fmt.Printf("❌ 加载配置失败: %v\n", err)
			os.Exit(1)
		}

		if cfg.ClientID == "" || (cfg.AccessToken == "" && cfg.RefreshToken == "") {
			fmt.Println("❌ 错误: 未检测到有效的登录凭证，无法导入。")
			fmt.Println("请先执行 `./spotify-backup login` 进行登录授权。")
			os.Exit(1)
		}

		client := spotify.NewClient(cfg, *configFlag)
		if err := backup.ImportPlaylists(client, *fileFlag, *prefixFlag, *suffixFlag); err != nil {
			fmt.Printf("❌ 导入失败: %v\n", err)
			os.Exit(1)
		}

	case "help", "-h", "--help":
		printUsage()

	default:
		fmt.Printf("未知子命令: %s\n", subcommand)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Spotify 歌单备份与恢复命令行工具")
	fmt.Println("用法: spotify-backup <子命令> [选项]")
	fmt.Println()
	fmt.Println("可用子命令:")
	fmt.Println("  login    - 启动本地 OAuth 授权流程，登录并保存 Token 到本地文件")
	fmt.Println("             选项:")
	fmt.Println("               -client-id   Spotify Application Client ID (可选，若不指定将交互式询问或读取环境变量)")
	fmt.Println("               -config      配置文件保存位置 (默认: credentials.json)")
	fmt.Println()
	fmt.Println("  export   - 导出歌单歌曲元数据到本地的 JSON/YAML 文件")
	fmt.Println("             选项:")
	fmt.Println("               -file, -f    备份输出文件路径 (默认: backup.json, 支持后缀 .yaml, .yml)")
	fmt.Println("               -config, -c  载入配置的 JSON 文件 (默认: credentials.json)")
	fmt.Println("               -playlist-ids 指定导出的歌单 ID，用逗号分隔 (默认全部)")
	fmt.Println()
	fmt.Println("  import   - 从备份文件恢复歌单到目标账号中")
	fmt.Println("             选项:")
	fmt.Println("               -file, -f    备份文件路径 (默认: backup.json)")
	fmt.Println("               -config, -c  载入配置的 JSON 文件 (默认: credentials.json)")
	fmt.Println("               -prefix      在创建的新歌单名称前面加入前缀 (默认无)")
	fmt.Println("               -suffix      在创建的新歌单名称后面加入后缀 (默认无，推荐测试使用如: _restore)")
	fmt.Println()
	fmt.Println("示例:")
	fmt.Println("  ./spotify-backup login -client-id 474a58b...")
	fmt.Println("  ./spotify-backup export -f my_playlists.yaml")
	fmt.Println("  ./spotify-backup import -f my_playlists.yaml -suffix _backup")
}
