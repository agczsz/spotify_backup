package spotify

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"spotify-backup/config"
)

const (
	RedirectURI = "http://127.0.0.1:8080/callback"
	AuthURL     = "https://accounts.spotify.com/authorize"
	TokenURL    = "https://accounts.spotify.com/api/token"
	Scopes      = "playlist-read-private playlist-read-collaborative playlist-modify-public playlist-modify-private user-library-read user-library-modify"
)

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// GenerateRandomString 生成指定字节长度的 Base64 编码随机字符串
func GenerateRandomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// generateCodeChallenge 传入 code_verifier 生成 code_challenge (S256)
func generateCodeChallenge(verifier string) string {
	h := sha256.New()
	h.Write([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// OpenBrowser 在默认浏览器中打开 URL (支持 Windows, macOS, Linux)
func OpenBrowser(targetURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", strings.ReplaceAll(targetURL, "&", "^&"))
	case "darwin":
		cmd = exec.Command("open", targetURL)
	default: // "linux"
		cmd = exec.Command("xdg-open", targetURL)
	}
	return cmd.Start()
}

// InteractiveLogin 启动本地服务进行 PKCE 交互式授权
func InteractiveLogin(clientID string) (*config.Config, error) {
	state, err := GenerateRandomString(16)
	if err != nil {
		return nil, fmt.Errorf("生成 state 失败: %w", err)
	}

	verifier, err := GenerateRandomString(32) // 32字节，约43个字符
	if err != nil {
		return nil, fmt.Errorf("生成 code_verifier 失败: %w", err)
	}

	challenge := generateCodeChallenge(verifier)

	// 用于接收授权码和错误的通道
	codeChan := make(chan string)
	errChan := make(chan error)

	// 创建临时 Web 服务器
	server := &http.Server{Addr: "127.0.0.1:8080"}

	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		retState := query.Get("state")
		code := query.Get("code")
		authErr := query.Get("error")

		if authErr != "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(fmt.Sprintf("<h1>授权失败</h1><p>错误原因: %s</p>", authErr)))
			errChan <- fmt.Errorf("spotify 授权错误: %s", authErr)
			return
		}

		if retState != state {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("<h1>授权校验失败</h1><p>State 校验不匹配，可能是 CSRF 攻击。</p>"))
			errChan <- fmt.Errorf("state 不匹配")
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<h1>授权成功！</h1><p>您已成功授权，现在可以关闭此页面，返回终端查看进度。</p>"))
		codeChan <- code
	})

	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			errChan <- fmt.Errorf("无法启动本地服务器: %w", err)
		}
	}()

	// 构建授权 URL
	u, _ := url.Parse(AuthURL)
	q := u.Query()
	q.Set("client_id", clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", RedirectURI)
	q.Set("code_challenge_method", "S256")
	q.Set("code_challenge", challenge)
	q.Set("scope", Scopes)
	q.Set("state", state)
	u.RawQuery = q.Encode()

	targetURL := u.String()
	fmt.Printf("正在自动打开浏览器进行授权...\n如果浏览器没有打开，请手动访问以下 URL:\n\n%s\n\n", targetURL)

	if err := OpenBrowser(targetURL); err != nil {
		fmt.Printf("警告: 自动打开浏览器失败 (%v)。请手动复制上方 URL 并在浏览器中访问。\n", err)
	}

	// 等待回调或超时
	var code string
	select {
	case code = <-codeChan:
		// 收到授权码
	case err := <-errChan:
		server.Close()
		return nil, err
	case <-time.After(5 * time.Minute):
		server.Close()
		return nil, fmt.Errorf("授权超时（5分钟未完成）")
	}

	// 优雅关闭 Web 服务器
	server.Close()

	// 使用授权码交换 Token
	return ExchangeToken(clientID, code, verifier)
}

// ExchangeToken 使用授权码和 verifier 交换 Token
func ExchangeToken(clientID, code, verifier string) (*config.Config, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {RedirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}

	resp, err := http.PostForm(TokenURL, data)
	if err != nil {
		return nil, fmt.Errorf("请求 Token 失败: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("交换 Token API 返回异常错误 (%d): %s", resp.StatusCode, string(bodyBytes))
	}

	var tr TokenResponse
	if err := json.Unmarshal(bodyBytes, &tr); err != nil {
		return nil, fmt.Errorf("解析 Token 响应失败: %w", err)
	}

	cfg := &config.Config{
		ClientID:     clientID,
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		TokenExpiry:  time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
	}

	return cfg, nil
}

// RefreshAccessToken 刷新 Access Token 并在过期时返回新的 Token
func RefreshAccessToken(clientID, refreshToken string) (string, time.Time, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	}

	resp, err := http.PostForm(TokenURL, data)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("请求刷新 Token 失败: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("刷新 Token API 返回异常错误 (%d): %s", resp.StatusCode, string(bodyBytes))
	}

	var tr TokenResponse
	if err := json.Unmarshal(bodyBytes, &tr); err != nil {
		return "", time.Time{}, fmt.Errorf("解析 Token 响应失败: %w", err)
	}

	expiry := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)

	// 注意：有些刷新请求可能不返回新的 RefreshToken，需要保留旧的
	// 如果返回了新的 RefreshToken，更新它
	newRefreshToken := refreshToken
	if tr.RefreshToken != "" {
		newRefreshToken = tr.RefreshToken
	}

	_ = newRefreshToken // 方便外部统一保存

	return tr.AccessToken, expiry, nil
}
