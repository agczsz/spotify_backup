package spotify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"spotify-backup/config"
)

func TestClient_RateLimitingAndRetry(t *testing.T) {
	var requestCount int32

	// 创建 Mock API 服务器
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)

		if count == 1 {
			// 第一轮请求：模拟 429 速率限制，要求等待 1 秒
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error": {"status": 429, "message": "API rate limit exceeded"}}`))
			return
		}

		if count == 2 {
			// 第二轮请求：模拟 500 临时服务端故障
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error": {"status": 500, "message": "Server Error"}}`))
			return
		}

		// 第三轮请求：成功返回
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "test_user_id", "display_name": "Test User"}`))
	}))
	defer ts.Close()

	// 初始化客户端配置
	cfg := &config.Config{
		ClientID:     "mock_client_id",
		AccessToken:  "mock_access_token",
		RefreshToken: "mock_refresh_token",
		TokenExpiry:  time.Now().Add(10 * time.Minute), // 未过期
	}

	client := NewClient(cfg, "")
	// 覆盖 HTTP 客户端以向 mock 服务器发送请求
	req, err := http.NewRequest("GET", ts.URL, nil)
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}

	startTime := time.Now()
	resp, err := client.Do(req)
	duration := time.Since(startTime)

	if err != nil {
		t.Fatalf("请求应该成功，但返回错误: %v", err)
	}
	defer resp.Body.Close()

	// 验证请求重试次数：第一轮 429 -> 第二轮 500 -> 第三轮 200
	finalCount := atomic.LoadInt32(&requestCount)
	if finalCount != 3 {
		t.Errorf("期望请求总次数为 3，实际为: %d", finalCount)
	}

	// 验证最终返回的内容
	var u User
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		t.Fatalf("解析响应 JSON 失败: %v", err)
	}

	if u.ID != "test_user_id" {
		t.Errorf("期望 ID 为 'test_user_id'，实际为: '%s'", u.ID)
	}

	// 验证等待时间：应至少大于 Retry-After (1s) + 500ms (500的退避时间) = 1.5s
	// 因为我们代码里 429 会额外多加 1s 缓冲 (WaitTime = RetryAfter + 1s = 2s)
	// 所以期望总等待时间至少为 2s (429 等待) + 500ms (500 指数退避等待) = 2.5s
	expectedMinDuration := 2 * time.Second
	if duration < expectedMinDuration {
		t.Errorf("期望重试等待时间至少为 %v，实际花费了: %v", expectedMinDuration, duration)
	}

	t.Logf("测试通过！总耗时: %v, 请求总数: %d", duration, finalCount)
}

func TestClient_TokenAutoRefresh(t *testing.T) {
	// 创建 Mock Token 刷新服务器
	// 注意 auth.go 中的 TokenURL 是写死的 https://accounts.spotify.com/api/token
	// 为了测试 ensureValidToken，我们模拟一个即将过期的配置，看看它是否会尝试请求刷新
	// 由于 auth.go 中 RefreshAccessToken 使用了固定的 TokenURL，在此我们主要验证 config 的时间判定逻辑。

	cfg := &config.Config{
		ClientID:     "test_id",
		AccessToken:  "old_token",
		RefreshToken: "", // 没有 refresh token
		TokenExpiry:  time.Now().Add(-10 * time.Minute), // 已过期
	}

	client := NewClient(cfg, "")

	// 由于没有 RefreshToken 且已过期，应该返回错误
	err := client.ensureValidToken()
	if err == nil {
		t.Error("期望 ensureValidToken 在没有 refresh token 且已过期时报错，但返回 nil")
	} else if !strings.Contains(err.Error(), "无 Refresh Token 可用") {
		t.Errorf("期望错误包含 '无 Refresh Token 可用'，实际为: %v", err)
	}
}
