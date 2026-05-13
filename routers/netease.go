package routers

import (
	"os"
	"fmt"
	"sync"
	"time"
	"regexp"
	"strings"
	"net/url"
	"encoding/json"
	"path/filepath"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

var reDiscFolder = regexp.MustCompile(`^(?i)(cd|disc|disk|vol|volume)[\s\._-]*\d+$`)

var globalAlbumJSONLocks sync.Map
var globalMemoryMetadataCache sync.Map

type memoryCacheItem struct {
	data      AlbumData
	expiresAt int64
}

func getOrFetchCompleteAlbumData(albumName, artistName, albumDir string) (AlbumData, bool) {
	isVirtual := albumDir == "" || strings.HasPrefix(albumDir, "virtual_")
	if albumDir == "" {
		albumDir = "virtual_" + cleanSearchTerm(albumName)
	}

	cleanAlb := cleanSearchTerm(albumName)
	cleanArt := cleanSearchTerm(artistName)
	cacheKey := fmt.Sprintf("%s:%s", strings.ToLower(cleanAlb), strings.ToLower(cleanArt))

	if cached, ok := globalMemoryMetadataCache.Load(cacheKey); ok {
		item := cached.(memoryCacheItem)
		if time.Now().Unix() < item.expiresAt {
			return item.data, true
		}
	}

	if !isVirtual {
		if data, found := getLocalAlbumData(albumDir); found {
			globalMemoryMetadataCache.Store(cacheKey, memoryCacheItem{
				data:      data,
				expiresAt: time.Now().Unix() + 600,
			})
			return data, true
		}
	}

	actual, loaded := globalAlbumJSONLocks.LoadOrStore(cacheKey, &sync.Mutex{})
	mu := actual.(*sync.Mutex)

	if loaded {
		mu.Lock()
		defer mu.Unlock()

		if cached, ok := globalMemoryMetadataCache.Load(cacheKey); ok {
			item := cached.(memoryCacheItem)
			if time.Now().Unix() < item.expiresAt {
				return item.data, true
			}
		}
		if !isVirtual {
			return getLocalAlbumData(albumDir)
		}
		return AlbumData{}, false
	}

	mu.Lock()
	defer func() {
		mu.Unlock()
		globalAlbumJSONLocks.Delete(cacheKey)
	}()

	if cached, ok := globalMemoryMetadataCache.Load(cacheKey); ok {
		item := cached.(memoryCacheItem)
		if time.Now().Unix() < item.expiresAt {
			return item.data, true
		}
	}
	if !isVirtual {
		if data, found := getLocalAlbumData(albumDir); found {
			globalMemoryMetadataCache.Store(cacheKey, memoryCacheItem{
				data:      data,
				expiresAt: time.Now().Unix() + 600,
			})
			return data, true
		}
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("⏳ [%s - %s] -> (推导物理节点: %s)", artistName, albumName, albumDir))
	
	fetchedData, err := fetchCompleteAlbumData(albumName, artistName, albumDir)
	
	if err == nil && fetchedData.AlbumID > 0 {
		globalMemoryMetadataCache.Store(cacheKey, memoryCacheItem{
			data:      fetchedData,
			expiresAt: time.Now().Unix() + 600,
		})

		if !isVirtual {
			if getConfigBool("enable_write_json", true) {
				saveLocalAlbumData(albumDir, fetchedData)
				pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ 成功写入元数据 JSON: %s", filepath.Join(albumDir, "netease_metadata.json")))
			}
		} else {
			pdk.Log(pdk.LogWarn, fmt.Sprintf("⚠️ 物理路径下探受阻，直接呈现: [%s - %s]", artistName, albumName))
		}

		return fetchedData, true
	}

	pdk.Log(pdk.LogError, fmt.Sprintf("❌ 元数据处理失败 [%s - %s]: %v", artistName, albumName, err))
	return AlbumData{}, false
}

func buildNeteaseHeaders(extra map[string]string) map[string]string {
	headers := map[string]string{"User-Agent": defaultUserAgent}
	cookieVal := getConfigString("netease_cookie", "")

	if cookieVal != "" && !strings.Contains(cookieVal, "MUSIC_U=") {
		cookieVal = "MUSIC_U=" + cookieVal
	}

	for k, v := range extra {
		if strings.ToLower(k) == "cookie" {
			if cookieVal != "" {
				headers[k] = v + "; " + cookieVal
			} else {
				headers[k] = v
			}
		} else {
			headers[k] = v
		}
	}

	if cookieVal != "" && headers["Cookie"] == "" {
		headers["Cookie"] = cookieVal
	}

	return headers
}

type SongData struct {
	ID       int64    `json:"ID"`
	Name     string   `json:"Name"`
	Work     string   `json:"Work"`
	TrackNum int      `json:"TrackNum"`
	DiscNum  int      `json:"DiscNum"`
	Artists  []string `json:"Artists"`
	ISRC     string   `json:"ISRC"`
	Genre    string   `json:"Genre"`
}

type AlbumData struct {
	AlbumID      int64      `json:"AlbumID"`
	AlbumName    string     `json:"AlbumName"`
	PicURL       string     `json:"PicURL"`
	ArtistPicURL string     `json:"ArtistPicURL"`
	Description  string     `json:"Description"`
	Company      string     `json:"Company"`
	PublishTime  int64      `json:"PublishTime"`
	PDFLink      string     `json:"PDFLink"`
	Songs        []SongData `json:"Songs"`
}

type IDCacheData struct {
	ID  int64  `json:"id"`
	Pic string `json:"pic"`
}

type searchResponse struct {
	Result struct {
		Songs []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Ar   []struct {
				Name string `json:"name"`
			} `json:"ar"`
			Al struct {
				ID     int64  `json:"id"`
				Name   string `json:"name"`
				PicURL string `json:"picUrl"`
			} `json:"al"`
			PublishTime int64 `json:"publishTime"`
		} `json:"songs"`
		Artists []struct {
			ID        int64  `json:"id"`
			Name      string `json:"name"`
			PicURL    string `json:"picUrl"`
			Img1v1Url string `json:"img1v1Url"`
		} `json:"artists"`
		Albums []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			PicURL string `json:"picUrl"`
			Size   int    `json:"size"`
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

func getLocalAlbumData(albumDir string) (AlbumData, bool) {
	b, err := os.ReadFile(filepath.Join(albumDir, "netease_metadata.json"))
	if err == nil {
		var data AlbumData
		if err := json.Unmarshal(b, &data); err == nil && data.AlbumID > 0 {
			return data, true
		}
	}
	return AlbumData{}, false
}

func saveLocalAlbumData(albumDir string, data AlbumData) {
	b, _ := json.MarshalIndent(data, "", "  ")
	os.WriteFile(filepath.Join(albumDir, "netease_metadata.json"), b, 0666)
}

func isTrackProcessed(albumDir, filename string) bool {
	content, err := os.ReadFile(filepath.Join(albumDir, "netease_processed.txt"))
	if err != nil {
		return false
	}
	return strings.Contains(string(content), filename+"\n")
}

func markTrackProcessed(albumDir, filename string) {
	if !getConfigBool("enable_write_processed", false) {
		return
	}

	f, err := os.OpenFile(filepath.Join(albumDir, "netease_processed.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		f.WriteString(filename + "\n")
		f.Close()
	}
}

func compactText(text string) string {
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	re := regexp.MustCompile(`\n\s*\n+`)
	text = re.ReplaceAllString(text, "\n")
	return strings.TrimSpace(text)
}

func cleanSongTitle(title string) string {
	reTrack := regexp.MustCompile(`^\s*\d+\s*[\.-]?\s*`)
	title = reTrack.ReplaceAllString(title, "")
	reBrackets := regexp.MustCompile(`(?i)[\[\(\{].*?(live|remix|version|edit|concert|mix|acoustic|instrumental).*?[\]\)\}]`)
	title = reBrackets.ReplaceAllString(title, "")
	reDash := regexp.MustCompile(`(?i)\s*[-—]\s*.*?(live|remix|version|edit|concert|mix|acoustic|instrumental).*`)
	title = reDash.ReplaceAllString(title, "")
	reEmptyBrackets := regexp.MustCompile(`[\[\(\{]\s*[\]\)\}]\s*$`)
	title = reEmptyBrackets.ReplaceAllString(title, "")
	return strings.TrimSpace(title)
}

func cleanSearchTerm(text string) string {
	re := regexp.MustCompile(`[\[\(].*?[\]\)]`)
	text = re.ReplaceAllString(text, " ")
	return strings.Join(strings.Fields(text), " ")
}

func fuzzyMatch(s1, s2 string) bool {
	re := regexp.MustCompile(`[^\p{L}\p{N}]+`)
	n1 := re.ReplaceAllString(strings.ToLower(s1), "")
	n2 := re.ReplaceAllString(strings.ToLower(s2), "")
	if n1 == "" || n2 == "" {
		return false
	}
	if n1 == n2 {
		return true
	}
	if len(n1) > 3 && len(n2) > 3 {
		if strings.Contains(n1, n2) || strings.Contains(n2, n1) {
			return true
		}
	}

	reAscii := regexp.MustCompile(`[^\x00-\x7F]+`)
	a1 := reAscii.ReplaceAllString(n1, "")
	a2 := reAscii.ReplaceAllString(n2, "")
	if len(a1) > 3 && len(a2) > 3 {
		if strings.Contains(a1, a2) || strings.Contains(a2, a1) {
			return true
		}
	}

	return false
}

func cleanLyric(text string) string {
	if text == "" {
		return ""
	}
	reBy := regexp.MustCompile(`\[by:.*?\]\n?`)
	text = reBy.ReplaceAllString(text, "")
	reAd := regexp.MustCompile(`(?i)\[\d{2}:\d{2}[\.:]\d{2,3}\].*?(www\.|.net|.com|翻译:|QQ:|微信:).*?\n?`)
	text = reAd.ReplaceAllString(text, "")
	reTime := regexp.MustCompile(`\[(\d{2}:\d{2})[\.:](\d{2})\d*\]`)
	text = reTime.ReplaceAllString(text, "[$1.$2]")
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

func extractWorkAndTitle(rawName string) (string, string) {
	cleanName := strings.ReplaceAll(rawName, "：", ":")
	parts := strings.SplitN(cleanName, ":", 2)
	if len(parts) > 1 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return "", strings.TrimSpace(rawName)
}

func smartSearchAPI(query string, searchType int) ([]byte, error) {
	pdk.Log(pdk.LogInfo, fmt.Sprintf("🌐 搜索关键词: %s", query))
	safeQuery := url.QueryEscape(query)
	reqURL := fmt.Sprintf("https://music.163.com/api/search/get/web?s=%s&type=%d&offset=0&limit=20", safeQuery, searchType)
	headers := buildNeteaseHeaders(map[string]string{"Referer": "https://music.163.com/"})

	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: reqURL, Headers: headers})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func smartAlbumDetailAPI(albumID int64) ([]byte, error) {
	pdk.Log(pdk.LogInfo, fmt.Sprintf("🌐 获取专辑详情 ID: %d", albumID))
	reqURL := fmt.Sprintf("https://music.163.com/api/v1/album/%d", albumID)
	headers := buildNeteaseHeaders(nil)

	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: reqURL, Headers: headers})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func smartSimiArtistAPI(artistID int64) ([]byte, error) {
	pdk.Log(pdk.LogInfo, fmt.Sprintf("🌐 获取相似艺人 ID: %d", artistID))
	payload := fmt.Sprintf("artistid=%d", artistID)

	resp, err := host.HTTPSend(host.HTTPRequest{
		Method:  "POST",
		URL:     "https://music.163.com/api/discovery/simiArtist",
		Headers: buildNeteaseHeaders(map[string]string{
			"Referer":      "https://music.163.com/",
			"Content-Type": "application/x-www-form-urlencoded",
		}),
		Body:    []byte(payload),
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func fetchCompleteAlbumData(albumName, artistName, albumDir string) (AlbumData, error) {
	var data AlbumData
	data.AlbumName = albumName

	cleanAlbum := cleanSearchTerm(albumName)
	cleanArtist := cleanSearchTerm(artistName)

	localTrackCount := 0
	if albumDir != "" {
		if entries, err := os.ReadDir(albumDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					ext := strings.ToLower(filepath.Ext(e.Name()))
					if ext == ".flac" || ext == ".mp3" || ext == ".m4a" || ext == ".alac" || ext == ".aac" {
						localTrackCount++
					}
				}
			}
		}
	}

	var albumID int64
	searchBody, errSearch := smartSearchAPI(cleanAlbum+" "+cleanArtist, 10)
	if errSearch == nil {
		var sr searchResponse
		json.Unmarshal(searchBody, &sr)

		if len(sr.Result.Albums) > 0 {
			bestIdx := -1
			minDiff := 9999
			bestSize := -1
			foundStrictMatch := false

			targetName := strings.TrimSpace(strings.ToLower(cleanAlbum))

			for i, al := range sr.Result.Albums {
				apiAlbumName := strings.TrimSpace(strings.ToLower(al.Name))
				
				isStrict := (apiAlbumName == targetName) || 
					(len(targetName) <= 3 && strings.HasPrefix(apiAlbumName, targetName))

				if localTrackCount > 0 {
					diff := al.Size - localTrackCount
					if diff < 0 {
						diff = -diff
					}

					if foundStrictMatch {
						if isStrict && diff < minDiff {
							minDiff = diff
							bestIdx = i
						}
					} else {
						if isStrict {
							foundStrictMatch = true
							minDiff = diff
							bestIdx = i
						} else if diff < minDiff {
							minDiff = diff
							bestIdx = i
						}
					}
				} else {
					if foundStrictMatch {
						if isStrict && al.Size > bestSize {
							bestSize = al.Size
							bestIdx = i
						}
					} else {
						if isStrict {
							foundStrictMatch = true
							bestSize = al.Size
							bestIdx = i
						} else if al.Size > bestSize {
							bestSize = al.Size
							bestIdx = i
						}
					}
				}
			}

			if bestIdx != -1 {
				albumID = sr.Result.Albums[bestIdx].ID
			} else {
				albumID = sr.Result.Albums[0].ID
			}
		}
	}

	if albumID == 0 {
		albumID, _, _ = resolveID(cleanAlbum, 10)
	}

	if albumID == 0 {
		return data, fmt.Errorf("album not found")
	}

	albumBody, errAlbum := smartAlbumDetailAPI(albumID)
	if errAlbum != nil {
		return data, errAlbum
	}

	var rawResp struct {
		Album struct {
			Id          int64  `json:"id"`
			Name        string `json:"name"`
			PicUrl      string `json:"picUrl"`
			Description string `json:"description"`
			Company     string `json:"company"`
			PublishTime int64  `json:"publishTime"`
		} `json:"album"`
		Songs []struct {
			Id   int64  `json:"id"`
			Name string `json:"name"`
			No   int    `json:"no"`
			Ar   []struct {
				Name string `json:"name"`
			} `json:"ar"`
		} `json:"songs"`
	}
	json.Unmarshal(albumBody, &rawResp)

	data.AlbumID = rawResp.Album.Id
	data.AlbumName = rawResp.Album.Name
	data.PicURL = rawResp.Album.PicUrl
	data.Description = compactText(rawResp.Album.Description)
	data.Company = rawResp.Album.Company
	data.PublishTime = rawResp.Album.PublishTime

	for _, s := range rawResp.Songs {
		var artists []string
		for _, a := range s.Ar {
			artists = append(artists, strings.TrimSpace(a.Name))
		}
		work, title := extractWorkAndTitle(s.Name)
		data.Songs = append(data.Songs, SongData{
			ID:       s.Id,
			Name:     title,
			Work:     work,
			TrackNum: s.No,
			Artists:  artists,
		})
	}

	_, artistPic, _ := resolveID(artistName, 100)
	data.ArtistPicURL = artistPic

	var cReqs []map[string]interface{}
	for _, s := range data.Songs {
		cReqs = append(cReqs, map[string]interface{}{"id": s.ID, "v": 0})
	}
	cBytes, _ := json.Marshal(cReqs)
	payload := "c=" + url.QueryEscape(string(cBytes))

	v3resp, errV3 := host.HTTPSend(host.HTTPRequest{
		Method:  "POST",
		URL:     "https://music.163.com/api/v3/song/detail",
		Headers: buildNeteaseHeaders(map[string]string{"Content-Type": "application/x-www-form-urlencoded"}),
		Body:    []byte(payload),
	})

	if errV3 == nil && v3resp != nil && v3resp.StatusCode == 200 {
		var v3Data struct {
			Songs []struct {
				Id int64  `json:"id"`
				Cd string `json:"cd"`
				No int    `json:"no"`
			} `json:"songs"`
		}
		json.Unmarshal(v3resp.Body, &v3Data)
		for _, v3s := range v3Data.Songs {
			for i, ds := range data.Songs {
				if ds.ID == v3s.Id {
					if v3s.No > 0 {
						data.Songs[i].TrackNum = v3s.No
					}
					var disc int
					fmt.Sscanf(v3s.Cd, "%d", &disc)
					if disc > 0 {
						data.Songs[i].DiscNum = disc
					}
				}
			}
		}
	}

	return data, nil
}

func resolveID(query string, searchType int) (int64, string, error) {
	cacheKey := fmt.Sprintf("id_map:%d:%s", searchType, strings.ToLower(query))
	var cached IDCacheData

	if data, ok := cacheGet(cacheKey); ok {
		if err := json.Unmarshal(data, &cached); err == nil && cached.ID > 0 {
			return cached.ID, cached.Pic, nil
		}
	}

	searchBody, err := smartSearchAPI(query, searchType)
	if err != nil {
		return 0, "", err
	}

	var sr searchResponse
	json.Unmarshal(searchBody, &sr)
	var foundID int64
	var foundPic string

	targetName := strings.ToLower(strings.TrimSpace(query))

	if searchType == 100 && len(sr.Result.Artists) > 0 {
		var exactMatches []int
		for i, art := range sr.Result.Artists {
			if strings.ToLower(strings.TrimSpace(art.Name)) == targetName {
				exactMatches = append(exactMatches, i)
			}
		}

		bestIdx := 0
		if len(exactMatches) > 1 {
			localAlbums := getLocalAlbumsForArtist(query)

			if len(localAlbums) > 0 {
				maxScore := -1
				for _, idx := range exactMatches {
					artID := sr.Result.Artists[idx].ID
					score := 0

					artRespRaw, _ := host.HTTPSend(host.HTTPRequest{
						Method:  "GET",
						URL:     fmt.Sprintf("https://music.163.com/api/v1/artist/%d", artID),
						Headers: buildNeteaseHeaders(nil),
					})

					if artRespRaw != nil && artRespRaw.StatusCode == 200 {
						var artResp struct {
							HotSongs []struct {
								Al struct {
									Name string `json:"name"`
								} `json:"al"`
							} `json:"hotSongs"`
						}
						json.Unmarshal(artRespRaw.Body, &artResp)

						apiAlbums := make(map[string]bool)
						for _, song := range artResp.HotSongs {
							if song.Al.Name != "" {
								apiAlbums[song.Al.Name] = true
							}
						}

						for apiAlb := range apiAlbums {
							for _, locAlb := range localAlbums {
								if fuzzyMatch(apiAlb, locAlb) {
									score++
									break
								}
							}
						}
					}

					if score > maxScore {
						maxScore = score
						bestIdx = idx
					}
				}
				pdk.Log(pdk.LogInfo, fmt.Sprintf("🔍 同名冲突 [%s]: 选中歌手 ID: %d", query, sr.Result.Artists[bestIdx].ID))
			} else {
				bestIdx = exactMatches[0]
			}
		} else if len(exactMatches) == 1 {
			bestIdx = exactMatches[0]
		}

		foundID = sr.Result.Artists[bestIdx].ID
		foundPic = sr.Result.Artists[bestIdx].Img1v1Url

		if foundPic == "" || foundPic == "None" {
			foundPic = sr.Result.Artists[bestIdx].PicURL
		}

	} else if searchType == 10 && len(sr.Result.Albums) > 0 {
		bestIdx := 0
		for i, al := range sr.Result.Albums {
			cleanAlName := strings.ToLower(strings.TrimSpace(al.Name))
			if cleanAlName == targetName {
				bestIdx = i
				break
			}
			if len(targetName) > len(cleanAlName) && cleanAlName != "" && strings.HasPrefix(targetName, cleanAlName) {
				bestIdx = i
				break
			}
		}
		foundID = sr.Result.Albums[bestIdx].ID
		foundPic = sr.Result.Albums[bestIdx].PicURL

	} else if searchType == 1 && len(sr.Result.Songs) > 0 {
		foundID = sr.Result.Songs[0].ID
		foundPic = sr.Result.Songs[0].Al.PicURL
	}

	if foundID != 0 {
		b, _ := json.Marshal(IDCacheData{ID: foundID, Pic: foundPic})
		cacheSet(cacheKey, b)
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
			pdfResp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: g.URL, Headers: map[string]string{"User-Agent": defaultUserAgent}})
			if err == nil && pdfResp.StatusCode == 200 {
				os.WriteFile(pdfPath, pdfResp.Body, 0666)
				pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ 成功下载 Qobuz PDF 至物理路径: %s", pdfPath))
			}
			return
		}
	}
}

func downloadImage(urlStr, savePath string) {
	if urlStr == "" || savePath == "" {
		return
	}
	if stat, err := os.Stat(savePath); err == nil && stat.Size() > 1024 {
		return
	}

	res := getConfigString("image_resolution", "1200")
	cleanURL := strings.Split(urlStr, "?")[0]
	fullPic := fmt.Sprintf("%s?param=%sy%s", strings.Replace(cleanURL, "http://", "https://", 1), res, res)
	
	resp, err := host.HTTPSend(host.HTTPRequest{
		Method:  "GET",
		URL:     fullPic,
		Headers: map[string]string{"User-Agent": defaultUserAgent},
	})

	if err == nil && resp != nil && resp.StatusCode == 200 && len(resp.Body) > 1024 {
		head := resp.Body
		isValidImg := false
		if len(head) > 4 {
			if head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF {
				isValidImg = true
			} else if head[0] == 0x89 && head[1] == 0x50 && head[2] == 0x4E && head[3] == 0x47 {
				isValidImg = true
			}
		}

		if isValidImg {
			os.WriteFile(savePath, resp.Body, 0666)
		} else {
			pdk.Log(pdk.LogWarn, fmt.Sprintf("⚠️ 图片似乎损坏，拒绝覆盖 (%s)", savePath))
		}
	}
}

func fetchAndWriteLocalLyrics(title, artist, album, absolutePath string, knownSongID int64) string {
	if absolutePath == "" {
		return ""
	}
	saveDir := filepath.Dir(absolutePath)
	ext := filepath.Ext(absolutePath)
	baseName := strings.TrimSuffix(filepath.Base(absolutePath), ext)
	lrcPath := filepath.Join(saveDir, baseName+".lrc")

	if content, err := os.ReadFile(lrcPath); err == nil {
		return string(content)
	}

	songID := knownSongID
	albumDir := getCleanAlbumDir(absolutePath)

	localData, found := getOrFetchCompleteAlbumData(album, artist, albumDir)

	if songID == 0 && found {
		fileName := filepath.Base(absolutePath)
		if matchedSong, foundSong := matchLocalFileToNeteaseSong(fileName, localData.Songs); foundSong && matchedSong.ID > 0 {
			songID = matchedSong.ID
		}
	}

	if songID == 0 {
		return ""
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("📄 获取歌词 ID: %d", songID))

	apiURL := "https://interface3.music.163.com/api/song/lyric"
	payload := fmt.Sprintf("id=%d&cp=false&tv=0&lv=0&rv=0&kv=0&yv=0&ytv=0&yrv=0", songID)
	resp, err := host.HTTPSend(host.HTTPRequest{
		Method:  "POST",
		URL:     apiURL,
		Headers: buildNeteaseHeaders(map[string]string{
			"Referer":      "https://music.163.com/",
			"Content-Type": "application/x-www-form-urlencoded",
			"Cookie":       "os=osx; osver=MacOS-14.3.1-arm; appver=2.0.3.131777",
		}),
		Body:    []byte(payload),
	})

	if err != nil || resp == nil || resp.StatusCode != 200 {
		return ""
	}

	var lrcResp lyricResponse
	if err := json.Unmarshal(resp.Body, &lrcResp); err != nil {
		return ""
	}

	lrcText := cleanLyric(lrcResp.Lrc.Lyric)
	tlyricText := cleanLyric(lrcResp.Tlyric.Lyric)

	if lrcText == "" {
		return ""
	}

	finalLyric := mergeTranslatedLyrics(lrcText, tlyricText)

	if getConfigBool("enable_write_lyrics", true) {
		os.WriteFile(lrcPath, []byte(finalLyric), 0666)
		pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ 歌词已写入本地: %s", lrcPath))
	}
	
	return finalLyric
}

func smartSimiSongAPI(songID int64) ([]byte, error) {
	pdk.Log(pdk.LogInfo, fmt.Sprintf("🌐 获取相似单曲 ID: %d", songID))
	reqURL := fmt.Sprintf("https://music.163.com/api/v1/discovery/simiSong?songid=%d", songID)
	headers := buildNeteaseHeaders(nil)

	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: reqURL, Headers: headers})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

type SimiSongRef struct {
	ID     int64
	Title  string
	Artist string
}

func fetchSimilarSongsBFS(seedSongID int64, targetCount int) []SimiSongRef {
	var results []SimiSongRef
	seenIDs := make(map[int64]bool)
	visitedSeeds := make(map[int64]bool)

	seenIDs[seedSongID] = true
	currentLayerSeeds := []int64{seedSongID}
	maxDepth := 3

	for depth := 0; depth < maxDepth; depth++ {
		var nextLayerSeeds []int64

		for _, seedID := range currentLayerSeeds {
			if visitedSeeds[seedID] {
				continue
			}
			visitedSeeds[seedID] = true

			body, err := smartSimiSongAPI(seedID)
			if err == nil {
				var rawResp struct {
					Songs []struct {
						ID      int64  `json:"id"`
						Name    string `json:"name"`
						Artists []struct {
							Name string `json:"name"`
						} `json:"artists"`
					} `json:"songs"`
				}

				if json.Unmarshal(body, &rawResp) == nil {
					for _, s := range rawResp.Songs {
						if !seenIDs[s.ID] {
							seenIDs[s.ID] = true

							artName := "Unknown Artist"
							if len(s.Artists) > 0 {
								artName = s.Artists[0].Name
							}

							results = append(results, SimiSongRef{
								ID:     s.ID,
								Title:  s.Name,
								Artist: artName,
							})

							nextLayerSeeds = append(nextLayerSeeds, s.ID)

							if len(results) >= targetCount {
								break
							}
						}
					}
				}
			}

			time.Sleep(300 * time.Millisecond)
			if len(results) >= targetCount {
				break
			}
		}

		if len(results) >= targetCount || len(nextLayerSeeds) == 0 {
			break
		}
		currentLayerSeeds = nextLayerSeeds
	}

	if len(results) > targetCount {
		results = results[:targetCount]
	}
	return results
}
