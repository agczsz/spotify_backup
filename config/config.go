package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Config 存储 Spotify API 的凭证信息
type Config struct {
	ClientID     string    `json:"client_id"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenExpiry  time.Time `json:"token_expiry"`
}

// LoadConfig 从环境变量或指定的 JSON 文件中加载配置
// 优先使用环境变量以适配 CI/GitHub Actions 环境
func LoadConfig(path string) (*Config, error) {
	// 尝试从环境变量加载
	envClientID := os.Getenv("SPOTIFY_CLIENT_ID")
	envRefreshToken := os.Getenv("SPOTIFY_REFRESH_TOKEN")
	envAccessToken := os.Getenv("SPOTIFY_ACCESS_TOKEN")

	if envClientID != "" && envRefreshToken != "" {
		cfg := &Config{
			ClientID:     envClientID,
			RefreshToken: envRefreshToken,
			AccessToken:  envAccessToken,
		}
		// 如果提供了 Access Token，假设它可以暂时使用
		if envAccessToken != "" {
			cfg.TokenExpiry = time.Now().Add(10 * time.Minute) // 假设10分钟后过期以触发自动刷新
		} else {
			cfg.TokenExpiry = time.Now().Add(-10 * time.Minute) // 已过期，强制刷新
		}
		return cfg, nil
	}

	// 环境变量不足，从本地文件加载
	if path == "" {
		path = "credentials.json"
	}

	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{}, nil // 返回空配置，需要 login
		}
		return nil, fmt.Errorf("无法打开配置文件: %w", err)
	}
	defer file.Close()

	var cfg Config
	if err := json.NewDecoder(file).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	return &cfg, nil
}

// SaveConfig 将配置保存到指定的 JSON 文件中
func SaveConfig(path string, cfg *Config) error {
	if path == "" {
		path = "credentials.json"
	}

	// 确保父目录存在
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("创建配置目录失败: %w", err)
		}
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("无法创建配置文件: %w", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(cfg); err != nil {
		return fmt.Errorf("编码配置文件失败: %w", err)
	}

	return nil
}

// PrintGitHubEnv 格式化输出 GitHub Secrets 需要填写的环境变量，方便用户复制
func (cfg *Config) PrintGitHubEnv() {
	fmt.Println("\n================= GitHub Secrets 环境变量 =================")
	fmt.Println("请在 GitHub 仓库的 Settings -> Secrets and variables -> Actions 中添加以下 Secrets:")
	fmt.Println()
	fmt.Printf("SPOTIFY_CLIENT_ID: %s\n", cfg.ClientID)
	fmt.Printf("SPOTIFY_REFRESH_TOKEN: %s\n", cfg.RefreshToken)
	fmt.Println()
	fmt.Println("==========================================================")
}

// SendWeComNotification 发送企业微信机器人消息 (采用 title, content, timestamp 载荷)
func SendWeComNotification(title, content string) {
	url := os.Getenv("WECOM_WEBHOOK")
	if url == "" {
		return // 若未配置 WECOM_WEBHOOK 环境变量，则直接跳过，不发送通知
	}
	if url == "disabled" || url == "none" {
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05")

	payload := map[string]string{
		"title":     title,
		"content":   content,
		"timestamp": timestamp,
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("⚠️ 编码 Webhook 消息 JSON 失败: %v\n", err)
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(jsonBytes))
	if err != nil {
		fmt.Printf("⚠️ 发送 Webhook 消息失败: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Printf("⚠️ Webhook 服务返回异常状态码 (%d): %s\n", resp.StatusCode, string(respBody))
	}
}
