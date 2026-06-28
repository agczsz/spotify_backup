package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"spotify-backup/config"
	"spotify-backup/spotify"

	"gopkg.in/yaml.v3"
)

type TrackBackup struct {
	URI        string   `yaml:"uri" json:"uri"`
	Name       string   `yaml:"name" json:"name"`
	Type       string   `yaml:"type" json:"type"`
	DurationMS int      `yaml:"duration_ms" json:"duration_ms"`
	Album      string   `yaml:"album" json:"album"`
	Artists    []string `yaml:"artists" json:"artists"`
}

type PlaylistBackup struct {
	ID            string        `yaml:"id" json:"id"`
	Name          string        `yaml:"name" json:"name"`
	Description   string        `yaml:"description" json:"description"`
	Public        bool          `yaml:"public" json:"public"`
	Collaborative bool          `yaml:"collaborative" json:"collaborative"`
	Tracks        []TrackBackup `yaml:"tracks" json:"tracks"`
}

type FileBackup struct {
	BackupDate string           `yaml:"backup_date" json:"backup_date"`
	Playlists  []PlaylistBackup `yaml:"playlists" json:"playlists"`
}

// ExportPlaylists 导出歌单为 JSON 或 YAML 文件
func ExportPlaylists(client *spotify.Client, outPath string, targetPlaylistIDs []string) error {
	fmt.Println("正在从 Spotify 获取歌单列表...")
	allPlaylists, err := client.GetPlaylists()
	if err != nil {
		errWrapped := fmt.Errorf("读取歌单列表失败: %w", err)
		notifyBackupFailure(errWrapped)
		return errWrapped
	}

	// 过滤目标歌单
	var playlistsToBackup []spotify.Playlist
	backupLikedSongs := false

	if len(targetPlaylistIDs) > 0 {
		idMap := make(map[string]bool)
		for _, id := range targetPlaylistIDs {
			idMap[id] = true
		}
		if idMap["LIKED_SONGS"] {
			backupLikedSongs = true
		}
		for _, p := range allPlaylists {
			if idMap[p.ID] {
				playlistsToBackup = append(playlistsToBackup, p)
			}
		}
		if len(playlistsToBackup) == 0 && !backupLikedSongs {
			errWrapped := fmt.Errorf("指定的歌单 ID 均未在账号中找到，请检查输入")
			notifyBackupFailure(errWrapped)
			return errWrapped
		}
	} else {
		playlistsToBackup = allPlaylists
		backupLikedSongs = true
	}

	totalPlaylistsCount := len(playlistsToBackup)
	if backupLikedSongs {
		totalPlaylistsCount++
	}

	fmt.Printf("开始备份 %d 个歌单（包含点赞歌曲）...\n", totalPlaylistsCount)
	var backedPlaylists []PlaylistBackup
	totalTracksCount := 0

	for idx, p := range playlistsToBackup {
		fmt.Printf("[%d/%d] 正在读取歌单: %s (共 %d 首歌曲)...\n", idx+1, len(playlistsToBackup), p.Name, p.GetTotalTracks())

		items, err := client.GetPlaylistItems(p.ID)
		if err != nil {
			fmt.Printf("⚠️ 读取歌单 %s 失败，将跳过该歌单。错误: %v\n", p.Name, err)
			continue
		}

		var tracks []TrackBackup
		for _, item := range items {
			tInfo := item.GetTrackInfo()
			// 过滤掉没有 track 对象的条目（例如被删除的曲目或 API 异常）
			if tInfo.URI == "" {
				continue
			}

			var artists []string
			for _, art := range tInfo.Artists {
				artists = append(artists, art.Name)
			}

			tracks = append(tracks, TrackBackup{
				URI:        tInfo.URI,
				Name:       tInfo.Name,
				Type:       tInfo.Type,
				DurationMS: tInfo.DurationMS,
				Album:      tInfo.Album.Name,
				Artists:    artists,
			})
		}

		backedPlaylists = append(backedPlaylists, PlaylistBackup{
			ID:            p.ID,
			Name:          p.Name,
			Description:   p.Description,
			Public:        p.Public,
			Collaborative: p.Collaborative,
			Tracks:        tracks,
		})

		totalTracksCount += len(tracks)
		fmt.Printf("   成功读取并记录了 %d 首歌曲\n", len(tracks))
		time.Sleep(100 * time.Millisecond) // 间歇休息下
	}

	if backupLikedSongs {
		fmt.Println("\n正在读取您的“已点赞的歌曲” (Saved Tracks)...")
		likedItems, err := client.GetLikedSongs()
		if err != nil {
			fmt.Printf("⚠️ 读取“已点赞的歌曲”失败，将跳过。错误: %v\n", err)
		} else {
			var tracks []TrackBackup
			for _, item := range likedItems {
				tInfo := item.GetTrackInfo()
				if tInfo.URI == "" {
					continue
				}

				var artists []string
				for _, art := range tInfo.Artists {
					artists = append(artists, art.Name)
				}

				tracks = append(tracks, TrackBackup{
					URI:        tInfo.URI,
					Name:       tInfo.Name,
					Type:       tInfo.Type,
					DurationMS: tInfo.DurationMS,
					Album:      tInfo.Album.Name,
					Artists:    artists,
				})
			}

			backedPlaylists = append(backedPlaylists, PlaylistBackup{
				ID:            "LIKED_SONGS",
				Name:          "已点赞的歌曲",
				Description:   "用户媒体库中点赞的歌曲列表 (Liked Songs)",
				Public:        false,
				Collaborative: false,
				Tracks:        tracks,
			})
			totalTracksCount += len(tracks)
			fmt.Printf("   成功读取并记录了 %d 首已点赞歌曲\n", len(tracks))
		}
	}

	backupData := FileBackup{
		BackupDate: time.Now().Format(time.RFC3339),
		Playlists:  backedPlaylists,
	}

	// 确保父目录存在
	dir := filepath.Dir(outPath)
	if dir != "." && dir != "" {
		_ = os.MkdirAll(dir, 0755)
	}

	file, err := os.Create(outPath)
	if err != nil {
		errWrapped := fmt.Errorf("创建备份文件失败: %w", err)
		notifyBackupFailure(errWrapped)
		return errWrapped
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(outPath))
	if ext == ".yaml" || ext == ".yml" {
		encoder := yaml.NewEncoder(file)
		encoder.SetIndent(2)
		if err := encoder.Encode(backupData); err != nil {
			errWrapped := fmt.Errorf("编码 YAML 失败: %w", err)
			notifyBackupFailure(errWrapped)
			return errWrapped
		}
	} else {
		// 默认保存为 JSON 格式
		encoder := json.NewEncoder(file)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(backupData); err != nil {
			errWrapped := fmt.Errorf("编码 JSON 失败: %w", err)
			notifyBackupFailure(errWrapped)
			return errWrapped
		}
	}

	// 发送 WeCom 成功备份通知
	var details []string
	for _, p := range backedPlaylists {
		details = append(details, fmt.Sprintf("- %s: %d 首 (ID: %s)", p.Name, len(p.Tracks), p.ID))
	}
	detailsText := strings.Join(details, "\n")

	title := "🟢 Spotify 歌单备份成功"
	content := fmt.Sprintf("文件: %s\n汇总: 成功备份 %d 个歌单 (共 %d 首曲目)\n\n备份详情:\n%s", 
		filepath.Base(outPath), len(backedPlaylists), totalTracksCount, detailsText)
	config.SendWeComNotification(title, content)

	fmt.Printf("\n✨ 备份完成！已保存 %d 个歌单，共计 %d 首曲目到文件: %s\n", len(backedPlaylists), totalTracksCount, outPath)
	return nil
}

func notifyBackupFailure(err error) {
	title := "🔴 Spotify 歌单备份失败"
	content := fmt.Sprintf("错误: %s\n建议: 请检查网络连接、配置文件，或重新运行 `login` 授权并更新 Secret。", err.Error())
	config.SendWeComNotification(title, content)
}

// ImportPlaylists 从备份文件还原歌单到 Spotify
func ImportPlaylists(client *spotify.Client, inpPath string, renamePrefix, renameSuffix string) error {
	file, err := os.Open(inpPath)
	if err != nil {
		return fmt.Errorf("无法打开备份文件: %w", err)
	}
	defer file.Close()

	var backupData FileBackup
	ext := strings.ToLower(filepath.Ext(inpPath))

	if ext == ".yaml" || ext == ".yml" {
		if err := yaml.NewDecoder(file).Decode(&backupData); err != nil {
			return fmt.Errorf("解析 YAML 备份失败: %w", err)
		}
	} else {
		if err := json.NewDecoder(file).Decode(&backupData); err != nil {
			return fmt.Errorf("解析 JSON 备份失败: %w", err)
		}
	}

	if len(backupData.Playlists) == 0 {
		return fmt.Errorf("备份文件中未找到任何有效的歌单记录")
	}

	// 获取当前登录用户 ID，用来作为创建歌单的 Owner
	fmt.Println("正在获取当前账号个人资料...")
	user, err := client.GetCurrentUser()
	if err != nil {
		return fmt.Errorf("获取用户信息失败: %w", err)
	}
	fmt.Printf("成功登录。目标导入账户: %s (ID: %s)\n", user.DisplayName, user.ID)

	for idx, p := range backupData.Playlists {
		if p.ID == "LIKED_SONGS" {
			fmt.Printf("\n[%d/%d] 正在准备还原“已点赞的歌曲” (共 %d 首)...\n", idx+1, len(backupData.Playlists), len(p.Tracks))

			var validURIs []string
			localCount := 0

			for _, track := range p.Tracks {
				if strings.HasPrefix(track.URI, "spotify:local:") {
					localCount++
					continue
				}
				validURIs = append(validURIs, track.URI)
			}

			if localCount > 0 {
				fmt.Printf("   ⚠️ 发现了 %d 首本地曲目，由于 Spotify 限制，无法保存到点赞歌曲，系统已自动跳过。\n", localCount)
			}

			if len(validURIs) > 0 {
				fmt.Printf("   正在保存 %d 首歌曲到您的媒体库（已点赞的歌曲）...\n", len(validURIs))
				if err := client.SaveTracksToLibrary(validURIs); err != nil {
					fmt.Printf("   ⚠️ 保存点赞歌曲到媒体库失败: %v\n", err)
					continue
				}
			}

			fmt.Println("✅ “已点赞的歌曲”还原成功！")
			time.Sleep(200 * time.Millisecond)
			continue
		}

		// 替换或添加名字的前后缀（支持测试同账号防冲突）
		newName := renamePrefix + p.Name + renameSuffix
		fmt.Printf("\n[%d/%d] 正在准备导入歌单: %s (原始歌单: %s)\n", idx+1, len(backupData.Playlists), newName, p.Name)

		// 区分出普通歌曲/节目与本地歌曲
		var validURIs []string
		localCount := 0

		for _, track := range p.Tracks {
			// 本地文件的 URI 格式如 spotify:local:artist:album:title:duration
			if strings.HasPrefix(track.URI, "spotify:local:") {
				localCount++
				continue
			}
			validURIs = append(validURIs, track.URI)
		}

		if localCount > 0 {
			fmt.Printf("   ⚠️ 发现了 %d 首本地曲目，由于 Spotify 限制，无法通过 Web API 导入，系统已自动跳过。\n", localCount)
		}

		if len(validURIs) == 0 {
			fmt.Println("   ⚠️ 歌单中没有可被导入的有效 Spotify 曲目，将直接创建空歌单。")
		}

		// 创建新的空歌单
		fmt.Printf("   正在创建新歌单: %s ...\n", newName)
		newPlaylistID, err := client.CreatePlaylist(user.ID, newName, p.Description, p.Public, p.Collaborative)
		if err != nil {
			fmt.Printf("   ⚠️ 创建新歌单 %s 失败，已跳过该歌单。错误: %v\n", newName, err)
			continue
		}

		// 添加曲目到新建的歌单中，严格保持原始备份的顺序
		if len(validURIs) > 0 {
			fmt.Printf("   正在添加 %d 首曲目...\n", len(validURIs))
			if err := client.AddPlaylistItems(newPlaylistID, validURIs); err != nil {
				fmt.Printf("   ⚠️ 向新歌单中添加曲目失败: %v\n", err)
				continue
			}
		}

		fmt.Printf("✅ 歌单 \"%s\" 导入成功！\n", newName)
		time.Sleep(200 * time.Millisecond) // 歌单间稍微缓和下频率
	}

	fmt.Println("\n✨ 所有歌单导入（恢复）操作已全部完成！")
	return nil
}
