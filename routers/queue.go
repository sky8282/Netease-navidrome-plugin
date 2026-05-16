package routers

import (
	"fmt"
	"path/filepath"

	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

func InitQueue() {
	pdk.Log(pdk.LogInfo, "🚀 专辑刮削拦截已启动 (WASM 分布式排队模式)")
}

func EnqueueAlbumTask(albumName, artistName string) {
	if albumName == "" {
		return
	}

	if Storage.IsAlbumLocked(albumName, artistName) {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("🛡️ 拦截重复请求: [%s - %s] 正在处理", artistName, albumName))
		return
	}
	Storage.SetAlbumLock(albumName, artistName)

	maxWorkers := getConfigInt("max_workers", 2)
	timeoutSeconds := 300 

	slotID := Storage.AcquireGlobalWorkerSlot(albumName, maxWorkers, timeoutSeconds)
	
	if slotID == -1 {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("⚠️ 全局排队超时放弃: [%s - %s] 排队超过 %d 秒", artistName, albumName, timeoutSeconds))
		return 
	}

	defer Storage.ReleaseGlobalWorkerSlot(slotID)

	pdk.Log(pdk.LogInfo, fmt.Sprintf("🟢 [取得并发槽位 %d] 开始同步拉取元数据: [%s - %s]", slotID, artistName, albumName))
	
	doAlbumScrape(albumName, artistName)
}

func doAlbumScrape(albumName, artistName string) {
	albumDir := resolveAlbumDir(albumName, artistName)
	if albumDir == "" {
		artistDir := resolveArtistDir(artistName)
		if artistDir != "" {
			_, artistPic, _ := resolveID(artistName, 100)

			if artistPic != "" {
				if getConfigBool("enable_write_artist_image", true) {
					downloadImage(artistPic, filepath.Join(artistDir, "artist.jpg"))
				}
				if getConfigBool("enable_write_global_artist_image", true) {
					saveGlobalArtistImage(artistName, artistPic)
				}
			}
		}
		return
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("⏳ 开始验证/生成 元数据: %s", albumDir))

	albumData, _ := getOrFetchCompleteAlbumData(albumName, artistName, albumDir)

	if albumData.AlbumID > 0 {
		if getConfigBool("enable_write_cover_image", true) {
			downloadImage(albumData.PicURL, filepath.Join(albumDir, "cover.jpg"))
		}
		if albumData.ArtistPicURL != "" {
			if getConfigBool("enable_write_artist_image", true) {
				downloadImage(albumData.ArtistPicURL, filepath.Join(filepath.Dir(albumDir), "artist.jpg"))
			}
			if getConfigBool("enable_write_global_artist_image", true) {
				saveGlobalArtistImage(artistName, albumData.ArtistPicURL)
			}
		}
	}
	
	pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ 专辑元数据处理完毕: [%s - %s]", artistName, albumName))
}