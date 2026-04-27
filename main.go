package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/metadata"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
)

const (
	neteaseBaseURL   = "https://music.163.com/api"
	defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_3) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"
)

type neteaseAgent struct{}

var (
	_ metadata.ArtistURLProvider       = (*neteaseAgent)(nil)
	_ metadata.ArtistBiographyProvider = (*neteaseAgent)(nil)
	_ metadata.ArtistImagesProvider    = (*neteaseAgent)(nil)
	_ metadata.SimilarArtistsProvider  = (*neteaseAgent)(nil)
	_ metadata.ArtistTopSongsProvider  = (*neteaseAgent)(nil)
	_ metadata.AlbumImagesProvider     = (*neteaseAgent)(nil)
	_ metadata.AlbumInfoProvider       = (*neteaseAgent)(nil)
	_ lyrics.Lyrics                    = (*neteaseAgent)(nil)
	_ scrobbler.Scrobbler              = (*neteaseAgent)(nil)
)

func init() {
	agent := &neteaseAgent{}
	metadata.Register(agent)
	lyrics.Register(agent)
	scrobbler.Register(agent)

	pdk.Log(pdk.LogInfo, "===============================================")
	pdk.Log(pdk.LogInfo, "💥 网易云插件触发启动！")
	pdk.Log(pdk.LogInfo, "===============================================")
}

func main() {}

func getConfigString(key, defaultVal string) string {
	val, ok := pdk.GetConfig(key)
	if !ok || val == "" {
		return defaultVal
	}
	return val
}

func getConfigBool(key string, defaultVal bool) bool {
	val, ok := pdk.GetConfig(key)
	if !ok || val == "" {
		return defaultVal
	}
	v := strings.ToLower(val)
	return v == "true" || v == "1" || v == "t" || v == "yes" || v == "y" || v == "on"
}

func debugLog(msg string) {
	if getConfigBool("enable_debug_log", true) {
		pdk.Log(pdk.LogInfo, "[Netease Debug] "+msg)
	}
}

func cleanSearchTerm(text string) string {
	re := regexp.MustCompile(`[\[\(].*?[\]\)]`)
	text = re.ReplaceAllString(text, " ")
	return strings.Join(strings.Fields(text), " ")
}

func cleanLyric(text string) string {
	if text == "" {
		return ""
	}
	reBy := regexp.MustCompile(`\[by:.*?\]\n?`)
	text = reBy.ReplaceAllString(text, "")
	reAd := regexp.MustCompile(`(?i)\[\d{2}:\d{2}[\.:]\d{2,3}\].*?(www\.|.net|.com|翻译:|QQ:|微信:).*?\n?`)
	text = reAd.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

func mergeTranslatedLyrics(lrc string, tlyric string) string {
	if tlyric == "" || lrc == "" {
		return lrc
	}
	pattern := regexp.MustCompile(`\[(\d{2}:\d{2})(?:\.\d{2,3})?\](.*)`)
	tagPattern := regexp.MustCompile(`\[(.*?)\]`)
	tMap := make(map[string]string)

	tLines := strings.Split(tlyric, "\n")
	for _, line := range tLines {
		matches := pattern.FindStringSubmatch(line)
		if len(matches) >= 3 {
			timeKey := matches[1]
			content := strings.TrimSpace(matches[2])
			if content != "" {
				tMap[timeKey] = content
			}
		}
	}

	var merged []string
	lLines := strings.Split(lrc, "\n")
	for _, line := range lLines {
		matches := pattern.FindStringSubmatch(line)
		if len(matches) >= 3 {
			timeKey := matches[1]
			originalText := strings.TrimSpace(matches[2])
			originalTimeTag := ""
			tagMatch := tagPattern.FindStringSubmatch(line)
			if len(tagMatch) >= 2 {
				originalTimeTag = tagMatch[1]
			}
			merged = append(merged, fmt.Sprintf("[%s]%s", originalTimeTag, originalText))
			if transText, exists := tMap[timeKey]; exists && transText != "" {
				merged = append(merged, fmt.Sprintf("[%s]%s", originalTimeTag, transText))
			}
		} else {
			merged = append(merged, line)
		}
	}
	return strings.Join(merged, "\n")
}

type searchResponse struct {
	Result struct {
		Songs []struct {
			ID    int64 `json:"id"`
			Album struct {
				PicURL string `json:"picUrl"`
			} `json:"album"`
			Al struct {
				PicURL string `json:"picUrl"`
			} `json:"al"`
		} `json:"songs"`
		Artists []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			PicURL string `json:"picUrl"`
		} `json:"artists"`
		Albums []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			PicURL string `json:"picUrl"`
		} `json:"albums"`
	} `json:"result"`
}

type lyricResponse struct {
	Lrc    struct{ Lyric string `json:"lyric"` } `json:"lrc"`
	Tlyric struct{ Lyric string `json:"lyric"` } `json:"tlyric"`
}

type qobuzSearchResponse struct {
	Albums struct {
		Items []struct{ ID string `json:"id"` } `json:"items"`
	} `json:"albums"`
}

type qobuzAlbumResponse struct {
	Goodies []struct {
		FileFormatID int    `json:"file_format_id"`
		Name         string `json:"name"`
		URL          string `json:"url"`
	} `json:"goodies"`
}

type artistDetailResponse struct {
	Artist struct {
		BriefDesc string `json:"briefDesc"`
	} `json:"artist"`
}

type albumDetailResponse struct {
	Album struct {
		Description string `json:"description"`
	} `json:"album"`
}

type simiArtistResponse struct {
	Artists []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"artists"`
}

type IDCacheData struct {
	ID  int64  `json:"id"`
	Pic string `json:"pic"`
}

func resolveID(query string, searchType int) (int64, string, error) {
	cacheKey := fmt.Sprintf("id_map:%d:%s", searchType, strings.ToLower(query))
	var cached IDCacheData
	
	data, exists, _ := host.KVStoreGet(cacheKey)
	if exists {
		if err := json.Unmarshal(data, &cached); err == nil && cached.ID > 0 {
			return cached.ID, cached.Pic, nil
		}
	}

	safeQuery := url.QueryEscape(query)
	apiURL := fmt.Sprintf("https://music.163.com/api/search/get/web?s=%s&type=%d&offset=0&limit=1", safeQuery, searchType)
	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: apiURL, Headers: map[string]string{"User-Agent": defaultUserAgent, "Referer": "https://music.163.com/"}})
	if err != nil {
		return 0, "", err
	}

	var sr searchResponse
	json.Unmarshal(resp.Body, &sr)
	var foundID int64
	var foundPic string
	
	if searchType == 100 && len(sr.Result.Artists) > 0 {
		foundID = sr.Result.Artists[0].ID
		foundPic = sr.Result.Artists[0].PicURL
	} else if searchType == 10 && len(sr.Result.Albums) > 0 {
		foundID = sr.Result.Albums[0].ID
		foundPic = sr.Result.Albums[0].PicURL
	} else if searchType == 1 && len(sr.Result.Songs) > 0 {
		foundID = sr.Result.Songs[0].ID
		if sr.Result.Songs[0].Album.PicURL != "" {
			foundPic = sr.Result.Songs[0].Album.PicURL
		} else if sr.Result.Songs[0].Al.PicURL != "" {
			foundPic = sr.Result.Songs[0].Al.PicURL
		}
	}

	if foundID != 0 {
		b, _ := json.Marshal(IDCacheData{ID: foundID, Pic: foundPic})
		host.KVStoreSet(cacheKey, b)
	}
	return foundID, foundPic, nil
}

func fetchQobuzPDFLink(albumName, artistName string) string {
	if !getConfigBool("enable_qobuz_pdf", true) {
		return ""
	}
	token := strings.TrimSpace(strings.Split(getConfigString("qobuz_auth_tokens", ""), ",")[0])
	if token == "" {
		return ""
	}
	
	cleanAlbum := cleanSearchTerm(albumName)
	cleanArtist := cleanSearchTerm(artistName)
	query := url.QueryEscape(cleanAlbum + " " + cleanArtist)
	
	headers := map[string]string{"X-App-Id": "798273057", "X-User-Auth-Token": token, "User-Agent": defaultUserAgent}
	respSearch, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: fmt.Sprintf("https://www.qobuz.com/api.json/0.2/catalog/search?query=%s&type=albums&limit=1", query), Headers: headers})
	if err != nil || respSearch.StatusCode != 200 {
		return ""
	}
	var sr qobuzSearchResponse
	json.Unmarshal(respSearch.Body, &sr)
	if len(sr.Albums.Items) == 0 {
		return ""
	}
	albumID := strings.ReplaceAll(sr.Albums.Items[0].ID, "qobuz_", "")
	respAlbum, _ := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: fmt.Sprintf("https://www.qobuz.com/api.json/0.2/album/get?album_id=%s&extra=focus", albumID), Headers: headers})
	var ar qobuzAlbumResponse
	json.Unmarshal(respAlbum.Body, &ar)

	for _, g := range ar.Goodies {
		n := strings.ToLower(g.Name)
		if (g.FileFormatID == 25 || g.FileFormatID == 21 || strings.Contains(n, "booklet")) && g.URL != "" {
			return fmt.Sprintf("<a href=\"%s\" style=\"color: #EAB308; font-weight: bold; text-decoration: underline;\" target=\"_blank\">点击下载 PDF</a>", g.URL)
		}
	}
	return ""
}

func downloadQobuzPDFToDisk(albumName, artistName, saveDir string) {
	if !getConfigBool("enable_write_pdf", true) || saveDir == "" {
		return
	}
	safeAlbumName := strings.ReplaceAll(strings.ReplaceAll(albumName, "/", "_"), "\\", "_")
	pdfPath := filepath.Join(saveDir, fmt.Sprintf("%s.pdf", safeAlbumName))
	
	if _, err := os.Stat(pdfPath); err == nil {
		debugLog(fmt.Sprintf("PDF 已存在，跳过: %s", pdfPath))
		return
	}

	token := strings.TrimSpace(strings.Split(getConfigString("qobuz_auth_tokens", ""), ",")[0])
	if token == "" {
		return
	}
	
	cleanAlbum := cleanSearchTerm(albumName)
	cleanArtist := cleanSearchTerm(artistName)
	query := url.QueryEscape(cleanAlbum + " " + cleanArtist)
	
	headers := map[string]string{"X-App-Id": "798273057", "X-User-Auth-Token": token, "User-Agent": defaultUserAgent}
	respSearch, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: fmt.Sprintf("https://www.qobuz.com/api.json/0.2/catalog/search?query=%s&type=albums&limit=1", query), Headers: headers})
	if err != nil || respSearch.StatusCode != 200 {
		return
	}
	var sr qobuzSearchResponse
	json.Unmarshal(respSearch.Body, &sr)
	if len(sr.Albums.Items) == 0 {
		return
	}
	albumID := strings.ReplaceAll(sr.Albums.Items[0].ID, "qobuz_", "")
	respAlbum, _ := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: fmt.Sprintf("https://www.qobuz.com/api.json/0.2/album/get?album_id=%s&extra=focus", albumID), Headers: headers})
	var ar qobuzAlbumResponse
	json.Unmarshal(respAlbum.Body, &ar)

	for _, g := range ar.Goodies {
		n := strings.ToLower(g.Name)
		if (g.FileFormatID == 25 || g.FileFormatID == 21 || strings.Contains(n, "booklet")) && g.URL != "" {
			debugLog(fmt.Sprintf("正在从 Qobuz 下载 PDF : %s", g.URL))
			pdfResp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: g.URL, Headers: map[string]string{"User-Agent": defaultUserAgent}})
			if err == nil && pdfResp.StatusCode == 200 {
				writeErr := os.WriteFile(pdfPath, pdfResp.Body, 0666)
				if writeErr == nil {
					pdk.Log(pdk.LogInfo, "[Netease Plugin] ✅ PDF 写入成功: "+pdfPath)
				} else {
					pdk.Log(pdk.LogError, fmt.Sprintf("[Netease Plugin] ❌ PDF 写入硬盘失败: %v", writeErr))
				}
			} else {
				debugLog(fmt.Sprintf("PDF 请求失败，状态码: %d", pdfResp.StatusCode))
			}
			return
		}
	}
}

func downloadCoverToDisk(title, album, artist, saveDir string) {
	if !getConfigBool("enable_write_cover_image", true) || saveDir == "" {
		return
	}
	coverPath := filepath.Join(saveDir, "cover.jpg")
	if _, err := os.Stat(coverPath); err == nil {
		debugLog(fmt.Sprintf("专辑封面已存在，跳过: %s", coverPath))
		return
	}

	cleanAlbum := cleanSearchTerm(album)
	cleanArtist := cleanSearchTerm(artist)
	
	query := fmt.Sprintf("%s %s", cleanAlbum, cleanArtist)
	_, picURL, _ := resolveID(query, 10)

	if picURL == "" && title != "" {
		songQuery := fmt.Sprintf("%s %s", title, cleanArtist)
		_, picURL, _ = resolveID(songQuery, 1)
	}

	if picURL == "" {
		debugLog("未能找到任何对应的专辑封面")
		return
	}

	res := getConfigString("image_resolution", "1200")
	fullPic := fmt.Sprintf("%s?param=%sy%s", strings.Replace(picURL, "http://", "https://", 1), res, res)
	
	debugLog("正在下载专辑封面...")
	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: fullPic, Headers: map[string]string{"User-Agent": defaultUserAgent}})
	if err == nil && resp.StatusCode == 200 {
		writeErr := os.WriteFile(coverPath, resp.Body, 0666)
		if writeErr == nil {
			pdk.Log(pdk.LogInfo, "[Netease Plugin] ✅ 专辑封面 cover.jpg 写入成功: "+coverPath)
		} else {
			pdk.Log(pdk.LogError, fmt.Sprintf("[Netease Plugin] ❌ 封面写入硬盘失败: %v", writeErr))
		}
	} else {
		debugLog("专辑封面下载请求失败")
	}
}

func downloadArtistToDisk(artist, saveDir string) {
	if !getConfigBool("enable_write_artist_image", true) || saveDir == "" {
		return
	}
	artistPath := filepath.Join(saveDir, "artist.jpg")
	if _, err := os.Stat(artistPath); err == nil {
		debugLog(fmt.Sprintf("歌手头像已存在，跳过: %s", artistPath))
		return
	}

	cleanArtist := cleanSearchTerm(artist)
	_, picURL, err := resolveID(cleanArtist, 100)
	if err != nil || picURL == "" {
		debugLog(fmt.Sprintf("未能找到歌手 %s 的头像", cleanArtist))
		return
	}

	res := getConfigString("image_resolution", "1200")
	fullPic := fmt.Sprintf("%s?param=%sy%s", strings.Replace(picURL, "http://", "https://", 1), res, res)
	
	debugLog("正在下载歌手头像...")
	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: fullPic, Headers: map[string]string{"User-Agent": defaultUserAgent}})
	if err == nil && resp.StatusCode == 200 {
		writeErr := os.WriteFile(artistPath, resp.Body, 0666)
		if writeErr == nil {
			pdk.Log(pdk.LogInfo, "[Netease Plugin] ✅ 歌手头像 artist.jpg 写入成功: "+artistPath)
		} else {
			pdk.Log(pdk.LogError, fmt.Sprintf("[Netease Plugin] ❌ 头像写入硬盘失败: %v", writeErr))
		}
	}
}

func fetchAndWriteLocalLyrics(title, artist, album, absolutePath string) string {
	if absolutePath == "" {
		return ""
	}
	saveDir := filepath.Dir(absolutePath)
	ext := filepath.Ext(absolutePath)
	baseName := strings.TrimSuffix(filepath.Base(absolutePath), ext)
	lrcPath := filepath.Join(saveDir, baseName+".lrc")

	if content, err := os.ReadFile(lrcPath); err == nil {
		debugLog(fmt.Sprintf("歌词文件已存在，直接读取: %s", lrcPath))
		return string(content)
	}

	debugLog(fmt.Sprintf("🎵 开始请求网易云歌词: %s - %s", artist, title))
	songID, _, err := resolveID(fmt.Sprintf("%s %s", title, cleanSearchTerm(artist)), 1)
	if err != nil || songID == 0 {
		debugLog("❌ 未能解析到对应的网易云歌曲 ID")
		return ""
	}

	apiURL := "https://interface3.music.163.com/api/song/lyric"
	payload := fmt.Sprintf("id=%d&cp=false&tv=0&lv=0&rv=0&kv=0&yv=0&ytv=0&yrv=0", songID)
	resp, err := host.HTTPSend(host.HTTPRequest{
		Method:  "POST",
		URL:     apiURL,
		Headers: map[string]string{"User-Agent": defaultUserAgent, "Referer": "https://music.163.com/", "Content-Type": "application/x-www-form-urlencoded", "Cookie": "os=pc"},
		Body:    []byte(payload),
	})
	if err != nil {
		return ""
	}

	var lrcResp lyricResponse
	json.Unmarshal(resp.Body, &lrcResp)
	lrcText := cleanLyric(lrcResp.Lrc.Lyric)
	tlyricText := cleanLyric(lrcResp.Tlyric.Lyric)
	if lrcText == "" {
		debugLog("⚠️ 网易云返回的歌词为空")
		return ""
	}
	finalLyric := mergeTranslatedLyrics(lrcText, tlyricText)

	if getConfigBool("enable_write_lyrics", true) {
		err = os.WriteFile(lrcPath, []byte(finalLyric), 0666)
		if err == nil {
			pdk.Log(pdk.LogInfo, "[Netease Plugin] ✅ LRC 歌词文件写入成功: "+lrcPath)
		} else {
			pdk.Log(pdk.LogError, fmt.Sprintf("[Netease Plugin] ❌ LRC 写入失败: %v", err))
		}
	}
	return finalLyric
}

func guessAlbumPath(albumName, artistName string) string {
	libraries, err := host.LibraryGetAllLibraries()
	if err != nil { return "" }
	for _, lib := range libraries {
		root := lib.MountPoint
		if root == "" { root = lib.Path }
		if root == "" { continue }
		
		guess1 := filepath.Join(root, artistName, albumName)
		if stat, err := os.Stat(guess1); err == nil && stat.IsDir() {
			return guess1
		}
		
		guess2 := filepath.Join(root, albumName)
		if stat, err := os.Stat(guess2); err == nil && stat.IsDir() {
			return guess2
		}
	}
	return ""
}

func guessArtistPath(artistName string) string {
	libraries, err := host.LibraryGetAllLibraries()
	if err != nil { return "" }
	for _, lib := range libraries {
		root := lib.MountPoint
		if root == "" { root = lib.Path }
		if root == "" { continue }
		guess := filepath.Join(root, artistName)
		if stat, err := os.Stat(guess); err == nil && stat.IsDir() {
			return guess
		}
	}
	return ""
}

type subsonicSongResponse struct {
	SubsonicResponse struct {
		Song struct {
			Path   string `json:"path"`
			Suffix string `json:"suffix"`
			Size   int64  `json:"size"`
		} `json:"song"`
	} `json:"subsonic-response"`
}

var errWalkStop = errors.New("stop walk")

func findAudioBySize(root, suffix string, size int64) (string, error) {
	if size <= 0 {
		return "", fmt.Errorf("无效文件大小")
	}
	dotSuffix := "." + suffix
	var found string

	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, dotSuffix) {
			return nil
		}
		if info.Size() == size {
			found = path
			return errWalkStop
		}
		return nil
	})

	if walkErr != nil && !errors.Is(walkErr, errWalkStop) {
		return "", walkErr
	}
	if found == "" {
		return "", fmt.Errorf("未找到对应文件")
	}
	return found, nil
}

func resolveAbsolutePath(username, trackID string) (string, error) {
	jsonStr, err := host.SubsonicAPICall("getSong?id=" + trackID + "&u=" + username + "&f=json")
	if err != nil {
		return "", err
	}

	var resp subsonicSongResponse
	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		return "", err
	}

	relPath := resp.SubsonicResponse.Song.Path
	suffix := resp.SubsonicResponse.Song.Suffix
	size := resp.SubsonicResponse.Song.Size

	if relPath == "" {
		return "", fmt.Errorf("获取相对路径失败")
	}

	libraries, err := host.LibraryGetAllLibraries()
	if err != nil {
		return "", fmt.Errorf("获取媒体库列表失败: %v", err)
	}

	for _, lib := range libraries {
		root := lib.MountPoint
		if root == "" {
			root = lib.Path
		}
		if root == "" {
			continue
		}

		direct := filepath.Join(root, relPath)
		if _, err := os.Stat(direct); err == nil {
			return direct, nil
		}

		if actualPath, searchErr := findAudioBySize(root, suffix, size); searchErr == nil {
			return actualPath, nil
		}
	}
	return "", fmt.Errorf("全库遍历未能定位到该文件的绝对物理路径")
}

func resolveFromRelativePath(relPath string) string {
	if relPath == "" || filepath.IsAbs(relPath) {
		return relPath
	}
	
	libraries, err := host.LibraryGetAllLibraries()
	if err == nil {
		for _, lib := range libraries {
			root := lib.MountPoint
			if root == "" {
				root = lib.Path
			}
			if root == "" {
				continue
			}
			
			fullPath := filepath.Join(root, relPath)
			if _, err := os.Stat(fullPath); err == nil {
				if absPath, err := filepath.Abs(fullPath); err == nil {
					return absPath
				}
				return fullPath 
			}
		}
	}
	
	if absFallback, err := filepath.Abs(relPath); err == nil {
		return absFallback
	}
	
	return relPath
}

func triggerPreciseBackgroundWrites(title, artist, album, absolutePath string) {
	if absolutePath == "" {
		return
	}
	albumDir := filepath.Dir(absolutePath)
	artistDir := filepath.Dir(albumDir)

	fetchAndWriteLocalLyrics(title, artist, album, absolutePath)
	downloadQobuzPDFToDisk(album, artist, albumDir)
	downloadCoverToDisk(title, album, artist, albumDir)
	downloadArtistToDisk(artist, artistDir)
}

func triggerHeuristicDownloads(albumName, artistName string) {
	albumDir := guessAlbumPath(albumName, artistName)
	if albumDir != "" {
		artistDir := filepath.Dir(albumDir)
		debugLog(fmt.Sprintf("推断专辑路径: %s", albumDir))
		downloadQobuzPDFToDisk(albumName, artistName, albumDir)
		downloadCoverToDisk("", albumName, artistName, albumDir)
		downloadArtistToDisk(artistName, artistDir)
	} else {
		artistDir := guessArtistPath(artistName)
		if artistDir != "" {
			downloadArtistToDisk(artistName, artistDir)
		}
	}
}

func (a *neteaseAgent) IsAuthorized(_ scrobbler.IsAuthorizedRequest) (bool, error) {
	return true, nil
}

func (a *neteaseAgent) NowPlaying(req scrobbler.NowPlayingRequest) error {
	var absolutePath string
	var err error

	if req.Username != "" {
		absolutePath, err = resolveAbsolutePath(req.Username, req.Track.ID)
	}

	if absolutePath == "" || err != nil {
		absolutePath = resolveFromRelativePath(req.Track.Path)
	}

	if absolutePath == "" {
		debugLog("已中止: 无法获取该歌曲的物理路径信息")
		return nil
	}
	
	debugLog(fmt.Sprintf("NowPlaying 触发拦截，并补全数据: %s", absolutePath))
	triggerPreciseBackgroundWrites(req.Track.Title, req.Track.Artist, req.Track.Album, absolutePath)
	return nil
}

func (a *neteaseAgent) Scrobble(req scrobbler.ScrobbleRequest) error {
	var absolutePath string
	var err error

	if req.Username != "" {
		absolutePath, err = resolveAbsolutePath(req.Username, req.Track.ID)
	}

	if absolutePath == "" || err != nil {
		absolutePath = resolveFromRelativePath(req.Track.Path)
	}

	if absolutePath == "" {
		debugLog("已中止: 无法获取该歌曲的物理路径信息")
		return nil
	}
	
	debugLog(fmt.Sprintf("Scrobble 触发拦截，并补全数据: %s", absolutePath))
	triggerPreciseBackgroundWrites(req.Track.Title, req.Track.Artist, req.Track.Album, absolutePath)
	return nil
}

func (a *neteaseAgent) GetLyrics(input lyrics.GetLyricsRequest) (lyrics.GetLyricsResponse, error) {
	if !getConfigBool("enable_lyrics", true) {
		return lyrics.GetLyricsResponse{}, nil
	}
	
	absPath := resolveFromRelativePath(input.Track.Path)
	
	triggerPreciseBackgroundWrites(input.Track.Title, input.Track.Artist, input.Track.Album, absPath)
	
	lyricText := fetchAndWriteLocalLyrics(input.Track.Title, input.Track.Artist, input.Track.Album, absPath)
	if lyricText == "" {
		return lyrics.GetLyricsResponse{}, nil
	}
	return lyrics.GetLyricsResponse{Lyrics: []lyrics.LyricsText{{Text: lyricText}}}, nil
}

func (a *neteaseAgent) GetAlbumInfo(input metadata.AlbumRequest) (*metadata.AlbumInfoResponse, error) {
	triggerHeuristicDownloads(input.Name, input.Artist)

	albumID, _, err := resolveID(fmt.Sprintf("%s %s", cleanSearchTerm(input.Name), cleanSearchTerm(input.Artist)), 10)
	if err != nil || albumID == 0 {
		return nil, nil
	}
	resp, _ := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: fmt.Sprintf("%s/v1/album/%d", neteaseBaseURL, albumID), Headers: map[string]string{"User-Agent": defaultUserAgent}})
	var detail albumDetailResponse
	json.Unmarshal(resp.Body, &detail)
	
	desc := strings.TrimSpace(detail.Album.Description)
	desc = strings.ReplaceAll(desc, "\r\n", "\n")
	desc = strings.ReplaceAll(desc, "\n", "<br>")
	for strings.Contains(desc, "<br><br>") {
		desc = strings.ReplaceAll(desc, "<br><br>", "<br>")
	}
	
	pdfLink := fetchQobuzPDFLink(input.Name, input.Artist)
	infoText := ""
	if pdfLink != "" {
		if desc != "" {
			infoText = pdfLink + "<br>" + desc
		} else {
			infoText = pdfLink
		}
	} else {
		infoText = desc
	}
	return &metadata.AlbumInfoResponse{Description: infoText}, nil
}

func (a *neteaseAgent) GetArtistBiography(input metadata.ArtistRequest) (*metadata.ArtistBiographyResponse, error) {
	artistID, _, err := resolveID(cleanSearchTerm(input.Name), 100)
	if err != nil || artistID == 0 {
		return nil, nil
	}
	apiURL := fmt.Sprintf("%s/v1/artist/%d", neteaseBaseURL, artistID)
	resp, _ := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: apiURL, Headers: map[string]string{"User-Agent": defaultUserAgent}})
	var detail artistDetailResponse
	json.Unmarshal(resp.Body, &detail)
	bio := strings.ReplaceAll(detail.Artist.BriefDesc, "\n", "<br>")
	return &metadata.ArtistBiographyResponse{Biography: bio}, nil
}

func (a *neteaseAgent) GetArtistImages(input metadata.ArtistRequest) (*metadata.ArtistImagesResponse, error) {
	_, picURL, err := resolveID(cleanSearchTerm(input.Name), 100)
	if err != nil || picURL == "" {
		return nil, nil
	}
	res := getConfigString("image_resolution", "1200")
	fullPic := fmt.Sprintf("%s?param=%sy%s", strings.Replace(picURL, "http://", "https://", 1), res, res)
	var size int32
	fmt.Sscanf(res, "%d", &size)
	return &metadata.ArtistImagesResponse{Images: []metadata.ImageInfo{{URL: fullPic, Size: size}}}, nil
}

func (a *neteaseAgent) GetAlbumImages(input metadata.AlbumRequest) (*metadata.AlbumImagesResponse, error) {
	query := fmt.Sprintf("%s %s", cleanSearchTerm(input.Name), cleanSearchTerm(input.Artist))
	_, picURL, err := resolveID(query, 10)
	if err != nil || picURL == "" {
		return nil, nil
	}
	res := getConfigString("image_resolution", "1200")
	fullPic := fmt.Sprintf("%s?param=%sy%s", strings.Replace(picURL, "http://", "https://", 1), res, res)
	var size int32
	fmt.Sscanf(res, "%d", &size)
	return &metadata.AlbumImagesResponse{Images: []metadata.ImageInfo{{URL: fullPic, Size: size}}}, nil
}

func (a *neteaseAgent) GetSimilarArtists(input metadata.SimilarArtistsRequest) (*metadata.SimilarArtistsResponse, error) {
	artistID, _, _ := resolveID(cleanSearchTerm(input.Name), 100)
	if artistID == 0 {
		return nil, nil
	}
	resp, _ := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: fmt.Sprintf("https://music.163.com/api/discovery/simiArtist?artistid=%d", artistID), Headers: map[string]string{"User-Agent": defaultUserAgent}})
	var sr simiArtistResponse
	json.Unmarshal(resp.Body, &sr)
	var res []metadata.ArtistRef
	for _, a := range sr.Artists {
		res = append(res, metadata.ArtistRef{Name: a.Name})
	}
	return &metadata.SimilarArtistsResponse{Artists: res}, nil
}

func (a *neteaseAgent) GetArtistURL(input metadata.ArtistRequest) (*metadata.ArtistURLResponse, error) {
	id, _, _ := resolveID(cleanSearchTerm(input.Name), 100)
	if id == 0 {
		return nil, nil
	}
	return &metadata.ArtistURLResponse{URL: fmt.Sprintf("https://music.163.com/#/artist?id=%d", id)}, nil
}

func (a *neteaseAgent) GetArtistTopSongs(input metadata.TopSongsRequest) (*metadata.TopSongsResponse, error) {
	return nil, nil
}