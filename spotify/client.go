package spotify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"spotify-backup/config"
)

type Client struct {
	cfg        *config.Config
	configPath string
	httpClient *http.Client
}

// SpotifyErrorResponse 对应 Spotify OpenAPI 中的错误返回格式
type SpotifyErrorResponse struct {
	ErrorDetails struct {
		Status  int    `json:"status"`
		Message string `json:"message"`
	} `json:"error"`
}

func (e *SpotifyErrorResponse) Error() string {
	return fmt.Sprintf("Spotify API 错误 (状态码 %d): %s", e.ErrorDetails.Status, e.ErrorDetails.Message)
}

type User struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type Playlist struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Public        bool   `json:"public"`
	Collaborative bool   `json:"collaborative"`
	TracksInfo    struct {
		Total int `json:"total"`
	} `json:"tracks"`
	ItemsInfo     struct {
		Total int `json:"total"`
	} `json:"items"`
}

func (p *Playlist) GetTotalTracks() int {
	if p.TracksInfo.Total > 0 {
		return p.TracksInfo.Total
	}
	return p.ItemsInfo.Total
}

type TrackInfo struct {
	URI        string   `json:"uri"`
	Name       string   `json:"name"`
	Type       string   `json:"type"` // "track" 或 "episode"
	DurationMS int      `json:"duration_ms"`
	Album      struct {
		Name string `json:"name"`
	} `json:"album"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
}

type PlaylistItem struct {
	AddedAt string    `json:"added_at"`
	Item    TrackInfo `json:"item"`  // 新版 API 字段
	Track   TrackInfo `json:"track"` // 旧版 API 字段
}

func (pi *PlaylistItem) GetTrackInfo() TrackInfo {
	if pi.Item.URI != "" {
		return pi.Item
	}
	return pi.Track
}

type PlaylistsPage struct {
	Items    []Playlist `json:"items"`
	Total    int        `json:"total"`
	Limit    int        `json:"limit"`
	Offset   int        `json:"offset"`
	Next     string     `json:"next"`
	Previous string     `json:"previous"`
}

type PlaylistItemsPage struct {
	Items  []PlaylistItem `json:"items"`
	Total  int            `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
	Next   string         `json:"next"`
}

func NewClient(cfg *config.Config, configPath string) *Client {
	return &Client{
		cfg:        cfg,
		configPath: configPath,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ensureValidToken 检查并确保 Token 有效，如果过期则自动刷新
func (c *Client) ensureValidToken() error {
	// 如果 AccessToken 和 RefreshToken 均为空，才是真正的未授权
	if c.cfg.AccessToken == "" && c.cfg.RefreshToken == "" {
		err := fmt.Errorf("未授权，请先运行 login 子命令进行登录")
		c.notifyTokenError(err)
		return err
	}

	// 如果 token 为空，或者将在 1 分钟内过期，或者已经过期，且有 refresh token，则进行刷新
	if c.cfg.AccessToken == "" || time.Now().Add(1 * time.Minute).After(c.cfg.TokenExpiry) {
		if c.cfg.RefreshToken == "" {
			err := fmt.Errorf("Access Token 已过期且无 Refresh Token 可用，请重新登录")
			c.notifyTokenError(err)
			return err
		}

		fmt.Println("Access Token 为空或已过期，正在刷新...")
		newAccess, expiry, err := RefreshAccessToken(c.cfg.ClientID, c.cfg.RefreshToken)
		if err != nil {
			wrappedErr := fmt.Errorf("自动刷新 Token 失败: %w", err)
			c.notifyTokenError(wrappedErr)
			return wrappedErr
		}

		c.cfg.AccessToken = newAccess
		c.cfg.TokenExpiry = expiry

		// 保存最新的 Token 凭证（在非 CI 环境变量运行时保存到本地）
		if c.configPath != "" && os.Getenv("SPOTIFY_REFRESH_TOKEN") == "" {
			if err := config.SaveConfig(c.configPath, c.cfg); err != nil {
				fmt.Printf("警告: 自动刷新后保存配置文件失败: %v\n", err)
			} else {
				fmt.Println("Token 刷新成功并已保存到本地。")
			}
		} else {
			fmt.Println("Token 刷新成功（使用环境变量凭证）。")
		}
	}
	return nil
}

func (c *Client) notifyTokenError(err error) {
	title := "⚠️ Spotify 备份工具授权已失效"
	content := fmt.Sprintf("原因: %s\n建议: 请在本地运行 `./spotify-backup login` 重新授权登录，并在 GitHub 配置最新 Secret。", err.Error())
	config.SendWeComNotification(title, content)
}

// Do 发送 HTTP 请求，自动处理 Token 刷新、429 速率限制重试、5xx 指数退避重试
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	// 1. 确保 Token 有效
	if err := c.ensureValidToken(); err != nil {
		return nil, err
	}

	// 2. 缓存请求体以防重试时需要重新读取
	var bodyBytes []byte
	if req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("读取请求体失败: %w", err)
		}
		req.Body.Close()
	}

	const maxRetries = 5
	backoff := 500 * time.Millisecond

	for attempt := 0; attempt < maxRetries; attempt++ {
		// 每次请求前重建 Body Reader
		if bodyBytes != nil {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		// 注入 Authorization 头
		req.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			// 如果是网络超时等临时错误，进行退避重试
			if attempt == maxRetries-1 {
				return nil, fmt.Errorf("请求发送失败（已重试 %d 次）: %w", maxRetries, err)
			}
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		// 3. 处理 429 Too Many Requests (速率限制)
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()

			retryAfterSec := 5 // 默认等待 5 秒
			if retryAfterStr := resp.Header.Get("Retry-After"); retryAfterStr != "" {
				if sec, err := strconv.Atoi(retryAfterStr); err == nil {
					retryAfterSec = sec
				}
			}

			// Spotify 限制很严格，多休眠 1 秒作为缓冲安全边界
			waitTime := time.Duration(retryAfterSec+1) * time.Second
			fmt.Printf("⚠️ 触发 Spotify API 速率限制 (HTTP 429)。根据 Retry-After 要求，休眠 %v 后重试...\n", waitTime)
			time.Sleep(waitTime)
			continue
		}

		// 4. 处理 5xx 服务端临时错误 (进行指数退避)
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			resp.Body.Close()
			if attempt == maxRetries-1 {
				return nil, fmt.Errorf("Spotify 服务器返回 5xx 错误（已重试 %d 次）: 状态码 %d", maxRetries, resp.StatusCode)
			}

			// 计算退避时间加一点抖动
			jitter := time.Duration(math.Min(float64(backoff), float64(5*time.Second)))
			fmt.Printf("⚠️ Spotify 服务端返回临时错误 (HTTP %d)，将在 %v 后重试...\n", resp.StatusCode, jitter)
			time.Sleep(jitter)
			backoff *= 2
			continue
		}

		// 5. 处理 4xx 及其他非 2xx 错误并读取具体原因
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			defer resp.Body.Close()
			errBytes, _ := io.ReadAll(resp.Body)

			var apiErr SpotifyErrorResponse
			if err := json.Unmarshal(errBytes, &apiErr); err == nil && apiErr.ErrorDetails.Message != "" {
				return nil, &apiErr
			}

			// 如果解析 JSON 错误失败，返回通用错误
			return nil, fmt.Errorf("Spotify API 返回异常状态码 (%d): %s", resp.StatusCode, string(errBytes))
		}

		// 成功返回
		return resp, nil
	}

	return nil, fmt.Errorf("请求重试耗尽，未能获得成功响应")
}

// GetCurrentUser 获取当前登录用户的 Profile 详情，主要用于拿到 User ID
func (c *Client) GetCurrentUser() (*User, error) {
	req, err := http.NewRequest("GET", "https://api.spotify.com/v1/me", nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("获取当前用户信息失败: %w", err)
	}
	defer resp.Body.Close()

	var u User
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, fmt.Errorf("解析用户信息响应失败: %w", err)
	}

	return &u, nil
}

// GetPlaylists 获取当前用户的所有歌单（包含私有歌单），支持自动分页
func (c *Client) GetPlaylists() ([]Playlist, error) {
	var playlists []Playlist
	nextURL := "https://api.spotify.com/v1/me/playlists?limit=50"

	for nextURL != "" {
		req, err := http.NewRequest("GET", nextURL, nil)
		if err != nil {
			return nil, err
		}

		resp, err := c.Do(req)
		if err != nil {
			return nil, fmt.Errorf("获取歌单分页失败: %w", err)
		}

		var page PlaylistsPage
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()

		if err != nil {
			return nil, fmt.Errorf("解析歌单分页 JSON 失败: %w", err)
		}

		playlists = append(playlists, page.Items...)
		nextURL = page.Next

		// 主动微小休眠，降低 API 请求密度
		if nextURL != "" {
			time.Sleep(100 * time.Millisecond)
		}
	}

	return playlists, nil
}

// GetPlaylistItems 获取指定歌单的所有歌曲/曲目，支持自动分页并严格保持原始排序顺序
func (c *Client) GetPlaylistItems(playlistID string) ([]PlaylistItem, error) {
	var items []PlaylistItem
	// 使用 /playlists/{id}/items 推荐端点替代废弃的 /tracks
	nextURL := fmt.Sprintf("https://api.spotify.com/v1/playlists/%s/items?limit=100", playlistID)

	for nextURL != "" {
		req, err := http.NewRequest("GET", nextURL, nil)
		if err != nil {
			return nil, err
		}

		resp, err := c.Do(req)
		if err != nil {
			return nil, fmt.Errorf("获取歌单歌曲分页失败: %w", err)
		}

		var page PlaylistItemsPage
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()

		if err != nil {
			return nil, fmt.Errorf("解析歌单歌曲分页 JSON 失败: %w", err)
		}

		items = append(items, page.Items...)
		nextURL = page.Next

		// 主动微小休眠
		if nextURL != "" {
			time.Sleep(100 * time.Millisecond)
		}
	}

	return items, nil
}

// CreatePlaylist 为指定用户创建一个新的歌单，并返回新歌单 ID
func (c *Client) CreatePlaylist(userID string, name, desc string, public, collaborative bool) (string, error) {
	apiURL := fmt.Sprintf("https://api.spotify.com/v1/users/%s/playlists", userID)

	bodyData := map[string]interface{}{
		"name":          name,
		"description":   desc,
		"public":        public,
		"collaborative": collaborative,
	}

	// 协作歌单要求必须为非公开歌单
	if collaborative {
		bodyData["public"] = false
	}

	jsonBytes, err := json.Marshal(bodyData)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(jsonBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("创建歌单请求失败: %w", err)
	}
	defer resp.Body.Close()

	var created Playlist
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", fmt.Errorf("解析新建歌单响应失败: %w", err)
	}

	return created.ID, nil
}

// AddPlaylistItems 向歌单中添加曲目列表（分批添加，每批最多 100 首）
func (c *Client) AddPlaylistItems(playlistID string, uris []string) error {
	if len(uris) == 0 {
		return nil
	}

	apiURL := fmt.Sprintf("https://api.spotify.com/v1/playlists/%s/items", playlistID)

	// 分批（Chunking），Spotify 单次请求最大支持 100 首
	chunkSize := 100
	for i := 0; i < len(uris); i += chunkSize {
		end := i + chunkSize
		if end > len(uris) {
			end = len(uris)
		}
		chunk := uris[i:end]

		bodyData := map[string]interface{}{
			"uris": chunk,
		}

		jsonBytes, err := json.Marshal(bodyData)
		if err != nil {
			return err
		}

		req, err := http.NewRequest("POST", apiURL, bytes.NewReader(jsonBytes))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.Do(req)
		if err != nil {
			return fmt.Errorf("添加歌曲到歌单失败 (第 %d-%d 首): %w", i+1, end, err)
		}
		resp.Body.Close()

		fmt.Printf("成功将第 %d-%d 首歌曲写入歌单中...\n", i+1, end)

		// 限制请求速率，微小休眠
		time.Sleep(150 * time.Millisecond)
	}

	return nil
}

// GetLikedSongs 获取当前用户已点赞的歌曲 (Saved Tracks)
func (c *Client) GetLikedSongs() ([]PlaylistItem, error) {
	var items []PlaylistItem
	nextURL := "https://api.spotify.com/v1/me/tracks?limit=50"

	for nextURL != "" {
		req, err := http.NewRequest("GET", nextURL, nil)
		if err != nil {
			return nil, err
		}

		resp, err := c.Do(req)
		if err != nil {
			return nil, fmt.Errorf("获取已点赞歌曲分页失败: %w", err)
		}

		var page PlaylistItemsPage
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()

		if err != nil {
			return nil, fmt.Errorf("解析已点赞歌曲分页 JSON 失败: %w", err)
		}

		items = append(items, page.Items...)
		nextURL = page.Next

		// 主动微小休眠
		if nextURL != "" {
			time.Sleep(100 * time.Millisecond)
		}
	}

	return items, nil
}

// SaveTracksToLibrary 将指定歌曲保存到用户媒体库（点赞歌曲），使用 PUT /v1/me/library
func (c *Client) SaveTracksToLibrary(uris []string) error {
	if len(uris) == 0 {
		return nil
	}

	// 限制每次最大 40 首 (Spotify /me/library 端点单次限制 40 首)
	chunkSize := 40
	for i := 0; i < len(uris); i += chunkSize {
		end := i + chunkSize
		if end > len(uris) {
			end = len(uris)
		}
		chunk := uris[i:end]

		// 拼接为逗号分隔的 URI
		encodedURIs := make([]string, len(chunk))
		for idx, u := range chunk {
			encodedURIs[idx] = url.QueryEscape(u)
		}
		queryVal := strings.Join(encodedURIs, ",")

		apiURL := fmt.Sprintf("https://api.spotify.com/v1/me/library?uris=%s", queryVal)

		req, err := http.NewRequest("PUT", apiURL, nil)
		if err != nil {
			return err
		}

		resp, err := c.Do(req)
		if err != nil {
			return fmt.Errorf("保存歌曲到媒体库失败 (第 %d-%d 首): %w", i+1, end, err)
		}
		resp.Body.Close()

		fmt.Printf("成功将第 %d-%d 首歌曲保存到您的“已点赞的歌曲”...\n", i+1, end)

		// 限制请求速率，微小休眠
		time.Sleep(150 * time.Millisecond)
	}

	return nil
}
