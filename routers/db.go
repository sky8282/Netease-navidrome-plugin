package routers

import (
	"fmt"
	"time"
	"encoding/json"
	
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

type DB struct{}

var Storage = &DB{}

type CacheWrapper struct {
	Timestamp int64           `json:"ts"`
	Payload   json.RawMessage `json:"payload"`
}

func (db *DB) SaveAlbumPath(albumName, artistName, dir string) {
	cacheKey := fmt.Sprintf("path_album_%s_%s", cleanSearchTerm(albumName), cleanSearchTerm(artistName))
	host.KVStoreSet(cacheKey, []byte(dir))
}

func (db *DB) GetAlbumPath(albumName, artistName string) (string, bool) {
	cacheKey := fmt.Sprintf("path_album_%s_%s", cleanSearchTerm(albumName), cleanSearchTerm(artistName))
	if data, ok, _ := host.KVStoreGet(cacheKey); ok {
		return string(data), true
	}
	return "", false
}

func (db *DB) SaveArtistPath(artistName, dir string) {
	cacheKey := fmt.Sprintf("path_artist_%s", cleanSearchTerm(artistName))
	host.KVStoreSet(cacheKey, []byte(dir))
}

func (db *DB) GetArtistPath(artistName string) (string, bool) {
	cacheKey := fmt.Sprintf("path_artist_%s", cleanSearchTerm(artistName))
	if data, ok, _ := host.KVStoreGet(cacheKey); ok {
		return string(data), true
	}
	return "", false
}

func (db *DB) SetCache(key string, data []byte) {
	wrap := CacheWrapper{
		Timestamp: time.Now().Unix(),
		Payload:   data,
	}
	b, _ := json.Marshal(wrap)
	host.KVStoreSet(key, b)
}

func (db *DB) GetCache(key string) ([]byte, bool) {
	b, ok, _ := host.KVStoreGet(key)
	if !ok {
		return nil, false
	}
	var wrap CacheWrapper
	if err := json.Unmarshal(b, &wrap); err == nil && wrap.Timestamp > 0 {
		days := getConfigInt("cache_days", 180)
		if time.Now().Unix()-wrap.Timestamp > int64(days*86400) {
			return nil, false
		}
		return wrap.Payload, true
	}
	return b, true
}

func (db *DB) SetTrackLock(absPath string) {
	lockKey := fmt.Sprintf("track_lock:%s", absPath)
	host.KVStoreSet(lockKey, []byte(fmt.Sprintf("%d", time.Now().Unix())))
}

func (db *DB) IsTrackLocked(absPath string) bool {
	lockKey := fmt.Sprintf("track_lock:%s", absPath)
	if lockData, ok, _ := host.KVStoreGet(lockKey); ok {
		var ts int64
		fmt.Sscanf(string(lockData), "%d", &ts)
		if time.Now().Unix()-ts < 15 {
			return true
		}
	}
	return false
}

func (db *DB) SaveIDMap(searchType int, query string, id int64, pic string) {
	cacheKey := fmt.Sprintf("id_map:%d:%s", searchType, query)
	b, _ := json.Marshal(IDCacheData{ID: id, Pic: pic})
	db.SetCache(cacheKey, b)
}

func (db *DB) GetIDMap(searchType int, query string) (IDCacheData, bool) {
	cacheKey := fmt.Sprintf("id_map:%d:%s", searchType, query)
	var cached IDCacheData
	if data, ok := db.GetCache(cacheKey); ok {
		if err := json.Unmarshal(data, &cached); err == nil && cached.ID > 0 {
			return cached, true
		}
	}
	return cached, false
}