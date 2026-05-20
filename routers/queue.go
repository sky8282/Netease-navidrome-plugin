package routers

import (
	"fmt"
	"sync"
	"time"
	"path/filepath"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

var trackLocks sync.Map 

func TryLockTrack(absPath string) bool {
	if absPath == "" { return false }
    
	if val, ok := trackLocks.Load(absPath); ok {
		lastTime := val.(int64)
		if time.Now().Unix()-lastTime < 900 { 
			return false 
		}
	}

	trackLocks.Store(absPath, time.Now().Unix())
	return true
}

func EnqueueTrackTask(absPath string, albumName, artistName string) {
	if absPath == "" { return }
    
	if !TryLockTrack(absPath) {
		return
	}
	
	doSingleTrackScrape(absPath, albumName, artistName)
}

func doSingleTrackScrape(absPath, albumName, artistName string) {
	doAlbumScrape(albumName, artistName)
}

func InitQueue() {
	//pdk.Log(pdk.LogInfo, "🚀 【视线焦点标记】")
}

func EnqueueAlbumTask(albumName, artistName string) {
	if albumName == "" {
		return
	}

	lockKey := "lock_album_" + cleanSearchTerm(albumName) + "_" + cleanSearchTerm(artistName)
	if lockData, ok, _ := host.KVStoreGet(lockKey); ok {
		var ts int64
		fmt.Sscanf(string(lockData), "%d", &ts)
		if time.Now().Unix()-ts < 900 {
			return 
		}
	}
	host.KVStoreSet(lockKey, []byte(fmt.Sprintf("%d", time.Now().Unix())))

	doAlbumScrape(albumName, artistName)
}

func doAlbumScrape(albumName, artistName string) {
	albumDir := resolveAlbumDir(albumName, artistName)
	if albumDir == "" {
		EnqueueArtistTask(artistName)
		return
	}

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
}

func EnqueueArtistTask(artistName string) {
	if artistName == "" {
		return
	}

	lockKey := "lock_artist_" + cleanSearchTerm(artistName)
	if lockData, ok, _ := host.KVStoreGet(lockKey); ok {
		var ts int64
		fmt.Sscanf(string(lockData), "%d", &ts)
		if time.Now().Unix()-ts < 900 {
			return 
		}
	}

	host.KVStoreSet(lockKey, []byte(fmt.Sprintf("%d", time.Now().Unix())))

	artistDir := resolveArtistDir(artistName)
	_, artistPic, _ := resolveID(artistName, 100)

	if artistPic != "" {
		if artistDir != "" && getConfigBool("enable_write_artist_image", true) {
			downloadImage(artistPic, filepath.Join(artistDir, "artist.jpg"))
		}
		if getConfigBool("enable_write_global_artist_image", true) {
			saveGlobalArtistImage(artistName, artistPic)
		}
	}
}