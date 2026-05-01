package main

import (
	"io"
	"os"
	"fmt"
	"time"
	"errors"
	"regexp"
	"strings"
	"net/url"
	"encoding/json"
	"path/filepath"

	"github.com/bogem/id3v2/v2"
	"github.com/go-flac/go-flac"
	"github.com/go-flac/flacvorbis"
	"github.com/Sorrow446/go-mp4tag"
	"github.com/go-flac/flacpicture"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/metadata"
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
	pdk.Log(pdk.LogInfo, "💥 Netease Plugin Started (On-Demand & JSON Cache Mode)")
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

func getConfigInt(key string, defaultVal int) int {
	val, ok := pdk.GetConfig(key)
	if !ok || val == "" {
		return defaultVal
	}
	var i int
	if _, err := fmt.Sscanf(val, "%d", &i); err != nil {
		return defaultVal
	}
	return i
}

func debugLog(msg string) {
	if getConfigBool("enable_debug_log", true) {
		pdk.Log(pdk.LogInfo, "[Netease Debug] "+msg)
	}
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

type CacheWrapper struct {
	Timestamp int64           `json:"ts"`
	Payload   json.RawMessage `json:"payload"`
}

func cacheSet(key string, data []byte) {
	wrap := CacheWrapper{
		Timestamp: time.Now().Unix(),
		Payload:   data,
	}
	b, _ := json.Marshal(wrap)
	host.KVStoreSet(key, b)
}

func cacheGet(key string) ([]byte, bool) {
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

type SongData struct {
	ID       int64    `json:"ID"`
	Name     string   `json:"Name"`
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
	safeQuery := url.QueryEscape(cleanAlbum + " " + cleanArtist)
	searchURL := fmt.Sprintf("https://music.163.com/api/search/get/web?s=%s&type=10&offset=0&limit=20", safeQuery)
	
	if resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: searchURL, Headers: buildNeteaseHeaders(map[string]string{"Referer": "https://music.163.com/"})}); err == nil {
		var sr searchResponse
		json.Unmarshal(resp.Body, &sr)
		
		if len(sr.Result.Albums) > 0 {
			bestIdx := -1
			minDiff := 9999
			bestSize := -1
			foundStrictMatch := false
			
			targetName := strings.TrimSpace(strings.ToLower(cleanAlbum))

			for i, al := range sr.Result.Albums {
				apiAlbumName := strings.TrimSpace(strings.ToLower(al.Name))
				isStrict := (apiAlbumName == targetName)

				if localTrackCount > 0 {
					diff := al.Size - localTrackCount
					if diff < 0 { diff = -diff }

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

	apiURL := fmt.Sprintf("https://music.163.com/api/v1/album/%d", albumID)
	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: apiURL, Headers: buildNeteaseHeaders(nil)})
	if err != nil {
		return data, err
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
	json.Unmarshal(resp.Body, &rawResp)

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
		data.Songs = append(data.Songs, SongData{
			ID:       s.Id,
			Name:     s.Name,
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
	v3resp, err := host.HTTPSend(host.HTTPRequest{
		Method:  "POST",
		URL:     "https://music.163.com/api/v3/song/detail",
		Headers: buildNeteaseHeaders(map[string]string{"Content-Type": "application/x-www-form-urlencoded"}),
		Body:    []byte(payload),
	})

	if err == nil {
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
					data.Songs[i].TrackNum = v3s.No
					var disc int
					fmt.Sscanf(v3s.Cd, "%d", &disc)
					data.Songs[i].DiscNum = disc
				}
			}
		}
	}
	
	return data, nil
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

func resolveID(query string, searchType int) (int64, string, error) {
	cacheKey := fmt.Sprintf("id_map:%d:%s", searchType, strings.ToLower(query))
	var cached IDCacheData
	
	if data, ok := cacheGet(cacheKey); ok {
		if err := json.Unmarshal(data, &cached); err == nil && cached.ID > 0 {
			return cached.ID, cached.Pic, nil
		}
	}

	safeQuery := url.QueryEscape(query)
	apiURL := fmt.Sprintf("https://music.163.com/api/search/get/web?s=%s&type=%d&offset=0&limit=1", safeQuery, searchType)
	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: apiURL, Headers: buildNeteaseHeaders(map[string]string{"Referer": "https://music.163.com/"})})
	if err != nil {
		return 0, "", err
	}

	var sr searchResponse
	json.Unmarshal(resp.Body, &sr)
	var foundID int64
	var foundPic string
	
	if searchType == 100 && len(sr.Result.Artists) > 0 {
		foundID = sr.Result.Artists[0].ID
		foundPic = sr.Result.Artists[0].Img1v1Url
		
		if foundPic == "" || foundPic == "None" {
			foundPic = sr.Result.Artists[0].PicURL
		}
		
	} else if searchType == 10 && len(sr.Result.Albums) > 0 {
		foundID = sr.Result.Albums[0].ID
		foundPic = sr.Result.Albums[0].PicURL
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
			}
			return
		}
	}
}

func downloadImage(urlStr, savePath string) {
	if urlStr == "" || savePath == "" {
		return
	}
	if _, err := os.Stat(savePath); err == nil {
		return
	}
	res := getConfigString("image_resolution", "1200")
	fullPic := fmt.Sprintf("%s?param=%sy%s", strings.Replace(urlStr, "http://", "https://", 1), res, res)
	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: fullPic, Headers: map[string]string{"User-Agent": defaultUserAgent}})
	if err == nil && resp.StatusCode == 200 {
		os.WriteFile(savePath, resp.Body, 0666)
	}
}

type lyricResponse struct {
	Lrc    struct{ Lyric string `json:"lyric"` } `json:"lrc"`
	Tlyric struct{ Lyric string `json:"lyric"` } `json:"tlyric"`
}

func fetchAndWriteLocalLyrics(title, artist, absolutePath string, knownSongID int64) string {
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
	if songID == 0 {
		songID, _, _ = resolveID(fmt.Sprintf("%s %s", title, cleanSearchTerm(artist)), 1)
	}

	if songID == 0 {
		return ""
	}

	apiURL := "https://interface3.music.163.com/api/song/lyric"
	payload := fmt.Sprintf("id=%d&cp=false&tv=0&lv=0&rv=0&kv=0&yv=0&ytv=0&yrv=0", songID)
	resp, err := host.HTTPSend(host.HTTPRequest{
		Method:  "POST",
		URL:     apiURL,
		Headers: buildNeteaseHeaders(map[string]string{"Referer": "https://music.163.com/", "Content-Type": "application/x-www-form-urlencoded", "Cookie": "os=pc"}),
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
		return ""
	}
	finalLyric := mergeTranslatedLyrics(lrcText, tlyricText)

	if getConfigBool("enable_write_lyrics", true) {
		os.WriteFile(lrcPath, []byte(finalLyric), 0666)
	}
	return finalLyric
}

func cleanFlacFile(absPath string) error {
	file, err := os.Open(absPath)
	if err != nil {
		return err
	}

	header := make([]byte, 10)
	if _, err := file.Read(header); err != nil {
		file.Close()
		return err
	}

	if string(header[0:3]) != "ID3" {
		file.Close()
		return fmt.Errorf("未检测到 ID3 头部")
	}

	size := (int(header[6]) << 21) | (int(header[7]) << 14) | (int(header[8]) << 7) | int(header[9])
	totalSize := int64(size + 10)

	magic := make([]byte, 4)
	if _, err := file.ReadAt(magic, totalSize); err != nil {
		file.Close()
		return err
	}

	if string(magic) != "fLaC" {
		file.Close()
		return fmt.Errorf("按协议计算大小后未找到真实的 fLaC 标识，跳过修复")
	}

	tempPath := absPath + ".tmp"
	tempFile, err := os.Create(tempPath)
	if err != nil {
		file.Close()
		return err
	}

	file.Seek(totalSize, 0)
	_, err = io.Copy(tempFile, file)
	tempFile.Close()
	file.Close()

	if err != nil {
		os.Remove(tempPath)
		return err
	}

	return os.Rename(tempPath, absPath)
}

func writeTags(absPath, ext string, song SongData, album AlbumData, year, comment, lyric string, picData []byte) bool {
	filename := filepath.Base(absPath)

	defer func() {
		if r := recover(); r != nil {
			pdk.Log(pdk.LogInfo, fmt.Sprintf("[TagWriter] [严重警告] 处理 %s 时底层库发生致命崩溃，已拦截跳过: %v", filename, r))
		}
	}()

	artistStr := strings.Join(song.Artists, "/")

	switch ext {
	case ".mp3":
		tag, err := id3v2.Open(absPath, id3v2.Options{Parse: true})
		if err != nil {
			pdk.Log(pdk.LogInfo, fmt.Sprintf("[TagWriter] [警告] 无法打开 MP3: %s, err: %v", filename, err))
			return false
		}
		defer tag.Close()
		tag.SetDefaultEncoding(id3v2.EncodingUTF8)

		changed := false

		if tag.Artist() == "" && artistStr != "" { tag.SetArtist(artistStr); changed = true }
		if tag.Album() == "" && album.AlbumName != "" { tag.SetAlbum(album.AlbumName); changed = true }
		if tag.Year() == "" && year != "" { tag.SetYear(year); changed = true }

		if len(tag.GetFrames("TRCK")) == 0 && song.TrackNum > 0 {
			tag.AddTextFrame("TRCK", id3v2.EncodingUTF8, fmt.Sprintf("%d", song.TrackNum))
			changed = true
		}
		if len(tag.GetFrames("TPOS")) == 0 && song.DiscNum > 0 {
			tag.AddTextFrame("TPOS", id3v2.EncodingUTF8, fmt.Sprintf("%d", song.DiscNum))
			changed = true
		}
		if len(tag.GetFrames("TPUB")) == 0 && album.Company != "" {
			tag.AddTextFrame("TPUB", id3v2.EncodingUTF8, album.Company)
			changed = true
		}
		if len(tag.GetFrames("TSRC")) == 0 && song.ISRC != "" {
			tag.AddTextFrame("TSRC", id3v2.EncodingUTF8, song.ISRC)
			changed = true
		}
		if len(tag.GetFrames("TCON")) == 0 && song.Genre != "" {
			tag.AddTextFrame("TCON", id3v2.EncodingUTF8, song.Genre)
			changed = true
		}

		hasComm := false
		for _, f := range tag.AllFrames() {
			for _, frame := range f {
				if _, ok := frame.(id3v2.CommentFrame); ok { hasComm = true }
			}
		}
		if !hasComm && comment != "" {
			tag.AddCommentFrame(id3v2.CommentFrame{Encoding: id3v2.EncodingUTF8, Language: "eng", Text: comment})
			changed = true
		}

		if len(tag.GetFrames(tag.CommonID("Unsynchronised lyrics/text transcription"))) == 0 && lyric != "" {
			tag.AddUnsynchronisedLyricsFrame(id3v2.UnsynchronisedLyricsFrame{Encoding: id3v2.EncodingUTF8, Language: "eng", Lyrics: lyric})
			changed = true
		}

		hasPic := false
		for _, f := range tag.AllFrames() {
			for _, frame := range f {
				if _, ok := frame.(id3v2.PictureFrame); ok { hasPic = true }
			}
		}
		if !hasPic && len(picData) > 0 {
			tag.AddAttachedPicture(id3v2.PictureFrame{
				Encoding:    id3v2.EncodingUTF8,
				MimeType:    "image/jpeg",
				PictureType: id3v2.PTFrontCover,
				Description: "Front Cover",
				Picture:     picData,
			})
			changed = true
		}

		if changed {
			if err := tag.Save(); err != nil { return false }
			pdk.Log(pdk.LogInfo, fmt.Sprintf("[TagWriter] 成功写入 MP3 标签: %s", filename))
			return true
		}
		return true

	case ".flac":
		f, err := flac.ParseFile(absPath)
		if err != nil {
			if strings.Contains(err.Error(), "fLaC head incorrect") {
				pdk.Log(pdk.LogInfo, fmt.Sprintf("[TagWriter] 检测到不规范的 FLAC 头部，尝试修复: %s", filename))
				if fixErr := cleanFlacFile(absPath); fixErr == nil {
					f, err = flac.ParseFile(absPath)
				}
			}
			if err != nil { return false }
		}

		var cmt *flacvorbis.MetaDataBlockVorbisComment
		for _, meta := range f.Meta {
			if meta.Type == flac.VorbisComment {
				cmt, _ = flacvorbis.ParseFromMetaDataBlock(*meta)
				break
			}
		}
		if cmt == nil { cmt = flacvorbis.New() }

		getFlacLen := func(key string) int { v, _ := cmt.Get(key); return len(v) }
		changed := false

		if getFlacLen("ARTIST") == 0 && len(song.Artists) > 0 {
			for _, a := range song.Artists { cmt.Add("ARTIST", a) }
			cmt.Add("ALBUMARTIST", artistStr)
			changed = true
		}
		if getFlacLen("ALBUM") == 0 && album.AlbumName != "" { cmt.Add("ALBUM", album.AlbumName); changed = true }
		if getFlacLen("DATE") == 0 && year != "" { cmt.Add("DATE", year); changed = true }
		if getFlacLen("TRACKNUMBER") == 0 && song.TrackNum > 0 { cmt.Add("TRACKNUMBER", fmt.Sprintf("%d", song.TrackNum)); changed = true }
		if getFlacLen("DISCNUMBER") == 0 && song.DiscNum > 0 { cmt.Add("DISCNUMBER", fmt.Sprintf("%d", song.DiscNum)); changed = true }
		if getFlacLen("ORGANIZATION") == 0 && getFlacLen("LABEL") == 0 && album.Company != "" {
			cmt.Add("ORGANIZATION", album.Company)
			cmt.Add("LABEL", album.Company)
			changed = true
		}
		if getFlacLen("ISRC") == 0 && song.ISRC != "" { cmt.Add("ISRC", song.ISRC); changed = true }
		if getFlacLen("GENRE") == 0 && song.Genre != "" { cmt.Add("GENRE", song.Genre); changed = true }
		if getFlacLen("COMMENT") == 0 && comment != "" { cmt.Add("COMMENT", comment); changed = true }
		if getFlacLen("LYRICS") == 0 && lyric != "" { cmt.Add("LYRICS", lyric); changed = true }

		hasPic := false
		var newMeta []*flac.MetaDataBlock
		for _, meta := range f.Meta {
			if meta.Type != flac.VorbisComment {
				if meta.Type == flac.Picture { hasPic = true }
				newMeta = append(newMeta, meta)
			}
		}

		if !hasPic && len(picData) > 0 {
			pic, err := flacpicture.NewFromImageData(flacpicture.PictureTypeFrontCover, "Front Cover", picData, "image/jpeg")
			if err == nil {
				picBlock := pic.Marshal()
				newMeta = append(newMeta, &picBlock)
				changed = true
			}
		}

		if changed {
			cmtBlock := cmt.Marshal()
			newMeta = append(newMeta, &cmtBlock)
			f.Meta = newMeta

			tempPath := absPath + ".tmp_tag"
			if err := f.Save(tempPath); err != nil {
				os.Remove(tempPath)
				return false
			}
			if err := os.Rename(tempPath, absPath); err != nil { return false }
			pdk.Log(pdk.LogInfo, fmt.Sprintf("[TagWriter] 成功写入 FLAC 标签: %s", filename))
			return true
		}
		return true

	case ".m4a", ".alac", ".aac":
		mp4, err := mp4tag.Open(absPath)
		if err != nil { return false }
		defer mp4.Close()
		tags, err := mp4.Read()
		if err != nil { tags = &mp4tag.MP4Tags{} }

		changed := false

		if tags.Artist == "" && artistStr != "" { tags.Artist = artistStr; changed = true }
		if tags.AlbumArtist == "" && artistStr != "" { tags.AlbumArtist = artistStr; changed = true }
		if tags.Album == "" && album.AlbumName != "" { tags.Album = album.AlbumName; changed = true }
		if tags.Date == "" && year != "" { tags.Date = year; changed = true }
		if tags.TrackNumber == 0 && song.TrackNum > 0 { tags.TrackNumber = int16(song.TrackNum); changed = true }
		if tags.DiscNumber == 0 && song.DiscNum > 0 { tags.DiscNumber = int16(song.DiscNum); changed = true }
		if tags.CustomGenre == "" && song.Genre != "" { tags.CustomGenre = song.Genre; changed = true }

		if tags.Custom == nil { tags.Custom = make(map[string]string) }
		if _, exists := tags.Custom["label"]; !exists && album.Company != "" { tags.Custom["label"] = album.Company; changed = true }
		if _, exists := tags.Custom["ISRC"]; !exists && song.ISRC != "" { tags.Custom["ISRC"] = song.ISRC; changed = true }

		if tags.Comment == "" && comment != "" { tags.Comment = comment; changed = true }
		if tags.Lyrics == "" && lyric != "" { tags.Lyrics = lyric; changed = true }

		if len(tags.Pictures) == 0 && len(picData) > 0 {
			tags.Pictures = []*mp4tag.MP4Picture{{Data: picData}}
			changed = true
		}

		if changed {
			if err := mp4.Write(tags, []string{}); err != nil { return false }
			pdk.Log(pdk.LogInfo, fmt.Sprintf("[TagWriter] 成功写入 M4A 标签: %s", filename))
			return true
		}
		return true
	}
	return false
}

func triggerAlbumPreload(albumName, artistName string) {
	lockKey := fmt.Sprintf("preload_lock:%s:%s", cleanSearchTerm(albumName), cleanSearchTerm(artistName))
	if lockData, ok, _ := host.KVStoreGet(lockKey); ok {
		var ts int64
		fmt.Sscanf(string(lockData), "%d", &ts)
		if time.Now().Unix()-ts < 10 { return }
	}
	host.KVStoreSet(lockKey, []byte(fmt.Sprintf("%d", time.Now().Unix())))

	albumDir := guessAlbumPath(albumName, artistName)
	if albumDir == "" {
		artistDir := guessArtistPath(artistName)
		if artistDir != "" {
			_, artistPic, _ := resolveID(artistName, 100)
			downloadImage(artistPic, filepath.Join(artistDir, "artist.jpg"))
		}
		return
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("[Phase1] 进入专辑页，开始获取并生成元数据 JSON: %s", albumDir))

	pdfLink := fetchQobuzPDFLink(albumName, artistName)
	
	var albumData AlbumData
	if localData, found := getLocalAlbumData(albumDir); found {
		albumData = localData
		if albumData.PDFLink == "" && pdfLink != "" {
			albumData.PDFLink = pdfLink
			saveLocalAlbumData(albumDir, albumData)
		}
		pdk.Log(pdk.LogInfo, "[Phase1] 本地 JSON 缓存已存在，跳过网易云 API 请求")
	} else {
		pdk.Log(pdk.LogInfo, "[Phase1] 本地无 JSON 缓存，正在拉取网易云 API...")
		fetchedData, err := fetchCompleteAlbumData(albumName, artistName, albumDir)
		if err == nil && fetchedData.AlbumID > 0 {
			fetchedData.PDFLink = pdfLink
			saveLocalAlbumData(albumDir, fetchedData)
			albumData = fetchedData
			pdk.Log(pdk.LogInfo, "[Phase1] API 拉取完成，成功生成 netease_metadata.json")
		}
	}

	if albumData.AlbumID > 0 {
		if getConfigBool("enable_write_cover_image", true) {
			downloadImage(albumData.PicURL, filepath.Join(albumDir, "cover.jpg"))
		}
		if getConfigBool("enable_write_artist_image", true) {
			downloadImage(albumData.ArtistPicURL, filepath.Join(filepath.Dir(albumDir), "artist.jpg"))
		}
	}
	pdk.Log(pdk.LogInfo, "[Phase1] 专辑元数据加载结束")
}

func fetchMetadataAndTag(absPath, title, artist, originalAlbum string) {
	if !getConfigBool("enable_write_metadata", true) { return }
	ext := strings.ToLower(filepath.Ext(absPath))
	if ext == ".wav" { return }

	lockKey := fmt.Sprintf("track_lock:%s", absPath)
	if lockData, ok, _ := host.KVStoreGet(lockKey); ok {
		var ts int64
		fmt.Sscanf(string(lockData), "%d", &ts)
		if time.Now().Unix()-ts < 15 {
			return 
		}
	}
	host.KVStoreSet(lockKey, []byte(fmt.Sprintf("%d", time.Now().Unix())))

	albumDir := filepath.Dir(absPath)
	fileName := filepath.Base(absPath)

	if isTrackProcessed(albumDir, fileName) {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("[Phase2] 跳过 (此曲目已完成写入): %s", fileName))
		return
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("[Phase2] 元数据正在处理并写入单曲: %s", fileName))

	var albumData AlbumData
	if localData, found := getLocalAlbumData(albumDir); found {
		albumData = localData
	} else {
		albumData, _ = fetchCompleteAlbumData(originalAlbum, artist, albumDir)
		albumData.PDFLink = fetchQobuzPDFLink(originalAlbum, artist)
		if albumData.AlbumID > 0 {
			saveLocalAlbumData(albumDir, albumData)
		}
	}

	if albumData.AlbumID == 0 { return }

	if getConfigBool("enable_write_cover_image", true) && albumData.PicURL != "" {
		downloadImage(albumData.PicURL, filepath.Join(albumDir, "cover.jpg"))
	}
	if getConfigBool("enable_write_artist_image", true) && albumData.ArtistPicURL != "" {
		downloadImage(albumData.ArtistPicURL, filepath.Join(filepath.Dir(albumDir), "artist.jpg"))
	}

	matchedSong, foundSong := matchLocalFileToNeteaseSong(fileName, albumData.Songs)
	if !foundSong {
		matchedSong = SongData{Artists: []string{artist}}
		matchedSong.Name = strings.TrimSuffix(fileName, ext)
	}

	lyricText := fetchAndWriteLocalLyrics(matchedSong.Name, artist, absPath, matchedSong.ID)

	var picData []byte
	if getConfigBool("enable_write_cover_image", true) {
		picData, _ = os.ReadFile(filepath.Join(albumDir, "cover.jpg"))
	}

	finalComment := albumData.Description
	if albumData.PDFLink != "" {
		actualURL := albumData.PDFLink
		if strings.HasPrefix(actualURL, "<a href=\"") {
			parts := strings.Split(actualURL, "\"")
			if len(parts) >= 2 {
				actualURL = parts[1]
			}
		}
		
		pdfTag := "PDF:" + actualURL
		
		if finalComment != "" {
			finalComment = albumData.Description + "\n\n" + pdfTag
		} else {
			finalComment = pdfTag
		}
	}

	year := ""
	if albumData.PublishTime > 0 {
		year = time.Unix(albumData.PublishTime/1000, 0).Format("2006")
	}

	isSuccess := writeTags(absPath, ext, matchedSong, albumData, year, finalComment, lyricText, picData)
	
	if isSuccess {
		markTrackProcessed(albumDir, fileName)
	}
}

func matchLocalFileToNeteaseSong(filename string, songs []SongData) (SongData, bool) {
	reNum := regexp.MustCompile(`^\s*0*(\d+)`)
	match := reNum.FindStringSubmatch(filename)
	var fileTrackNum int
	if len(match) > 1 { fmt.Sscanf(match[1], "%d", &fileTrackNum) }

	if fileTrackNum > 0 {
		for _, s := range songs {
			if s.TrackNum == fileTrackNum { return s, true }
		}
	}
	for _, s := range songs {
		if fuzzyMatch(filename, s.Name) { return s, true }
	}
	return SongData{}, false
}

func guessAlbumPath(albumName, artistName string) string {
	libraries, err := host.LibraryGetAllLibraries()
	if err != nil { return "" }
	for _, lib := range libraries {
		root := lib.MountPoint
		if root == "" { root = lib.Path }
		if root == "" { continue }
		
		guess1 := filepath.Join(root, artistName, albumName)
		if stat, err := os.Stat(guess1); err == nil && stat.IsDir() { return guess1 }
		
		guess2 := filepath.Join(root, albumName)
		if stat, err := os.Stat(guess2); err == nil && stat.IsDir() { return guess2 }

		artistGuess := filepath.Join(root, artistName)
		if stat, err := os.Stat(artistGuess); err == nil && stat.IsDir() {
			if subEntries, err := os.ReadDir(artistGuess); err == nil {
				for _, sub := range subEntries {
					if sub.IsDir() && (fuzzyMatch(albumName, sub.Name()) || strings.Contains(strings.ToLower(sub.Name()), strings.ToLower(albumName))) {
						return filepath.Join(artistGuess, sub.Name())
					}
				}
			}
		}

		if entries, err := os.ReadDir(root); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() { continue }
				
				isArtistDir := fuzzyMatch(artistName, entry.Name()) || strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(artistName))
				if isArtistDir {
					artistDir := filepath.Join(root, entry.Name())
					if subEntries, err := os.ReadDir(artistDir); err == nil {
						for _, sub := range subEntries {
							if sub.IsDir() && (fuzzyMatch(albumName, sub.Name()) || strings.Contains(strings.ToLower(sub.Name()), strings.ToLower(albumName))) {
								return filepath.Join(artistDir, sub.Name())
							}
						}
					}
				}
				
				if fuzzyMatch(albumName, entry.Name()) || strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(albumName)) {
					return filepath.Join(root, entry.Name())
				}
			}
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
		if stat, err := os.Stat(guess); err == nil && stat.IsDir() { return guess }
		if entries, err := os.ReadDir(root); err == nil {
			for _, entry := range entries {
				if entry.IsDir() && (fuzzyMatch(artistName, entry.Name()) || strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(artistName))) {
					return filepath.Join(root, entry.Name())
				}
			}
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
	if size <= 0 { return "", fmt.Errorf("invalid size") }
	dotSuffix := "." + suffix
	var found string
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, dotSuffix) { return nil }
		if info.Size() == size { found = path; return errWalkStop }
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errWalkStop) { return "", walkErr }
	if found == "" { return "", fmt.Errorf("not found") }
	return found, nil
}

func resolveAbsolutePath(username, trackID string) (string, error) {
	jsonStr, err := host.SubsonicAPICall("getSong?id=" + trackID + "&u=" + username + "&f=json")
	if err != nil { return "", err }
	var resp subsonicSongResponse
	json.Unmarshal([]byte(jsonStr), &resp)
	relPath := resp.SubsonicResponse.Song.Path
	suffix := resp.SubsonicResponse.Song.Suffix
	size := resp.SubsonicResponse.Song.Size
	if relPath == "" { return "", fmt.Errorf("relpath failed") }
	libraries, _ := host.LibraryGetAllLibraries()
	for _, lib := range libraries {
		root := lib.MountPoint
		if root == "" { root = lib.Path }
		if root == "" { continue }
		direct := filepath.Join(root, relPath)
		if _, err := os.Stat(direct); err == nil { return direct, nil }
		if actualPath, searchErr := findAudioBySize(root, suffix, size); searchErr == nil { return actualPath, nil }
	}
	return "", fmt.Errorf("not found absolute")
}

func resolveFromRelativePath(relPath string) string {
	if relPath == "" || filepath.IsAbs(relPath) { return relPath }
	libraries, err := host.LibraryGetAllLibraries()
	if err == nil {
		for _, lib := range libraries {
			root := lib.MountPoint
			if root == "" { root = lib.Path }
			if root == "" { continue }
			fullPath := filepath.Join(root, relPath)
			if _, err := os.Stat(fullPath); err == nil {
				if absPath, err := filepath.Abs(fullPath); err == nil { return absPath }
				return fullPath 
			}
		}
	}
	if absFallback, err := filepath.Abs(relPath); err == nil { return absFallback }
	return relPath
}

func (a *neteaseAgent) IsAuthorized(_ scrobbler.IsAuthorizedRequest) (bool, error) { return true, nil }

func (a *neteaseAgent) NowPlaying(req scrobbler.NowPlayingRequest) error {
	abs, _ := resolveAbsolutePath(req.Username, req.Track.ID)
	if abs == "" { abs = resolveFromRelativePath(req.Track.Path) }
	if abs != "" {
		fetchMetadataAndTag(abs, req.Track.Title, req.Track.Artist, req.Track.Album)
	}
	return nil
}

func (a *neteaseAgent) Scrobble(req scrobbler.ScrobbleRequest) error {
	abs, _ := resolveAbsolutePath(req.Username, req.Track.ID)
	if abs == "" { abs = resolveFromRelativePath(req.Track.Path) }
	if abs != "" {
		fetchMetadataAndTag(abs, req.Track.Title, req.Track.Artist, req.Track.Album)
	}
	return nil
}

func (a *neteaseAgent) GetLyrics(input lyrics.GetLyricsRequest) (lyrics.GetLyricsResponse, error) {
	if !getConfigBool("enable_lyrics", true) { return lyrics.GetLyricsResponse{}, nil }
	abs := resolveFromRelativePath(input.Track.Path)
	if abs != "" {
		fetchMetadataAndTag(abs, input.Track.Title, input.Track.Artist, input.Track.Album)
	}
	lyricText := fetchAndWriteLocalLyrics(input.Track.Title, input.Track.Artist, abs, 0)
	if lyricText == "" { return lyrics.GetLyricsResponse{}, nil }
	return lyrics.GetLyricsResponse{Lyrics: []lyrics.LyricsText{{Text: lyricText}}}, nil
}

func (a *neteaseAgent) GetAlbumInfo(input metadata.AlbumRequest) (*metadata.AlbumInfoResponse, error) {
	triggerAlbumPreload(input.Name, input.Artist)

	albumDir := guessAlbumPath(input.Name, input.Artist)
	if localData, found := getLocalAlbumData(albumDir); found {
		desc := strings.ReplaceAll(localData.Description, "\n", "<br>")
		if localData.PDFLink != "" {
			return &metadata.AlbumInfoResponse{Description: localData.PDFLink + "<br>" + desc}, nil
		}
		return &metadata.AlbumInfoResponse{Description: desc}, nil
	}
	
	albumID, _, _ := resolveID(fmt.Sprintf("%s %s", cleanSearchTerm(input.Name), cleanSearchTerm(input.Artist)), 10)
	if albumID == 0 { return nil, nil }
	resp, _ := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: fmt.Sprintf("%s/v1/album/%d", neteaseBaseURL, albumID), Headers: buildNeteaseHeaders(nil)})
	var detail struct { Album struct { Description string `json:"description"` } `json:"album"` }
	json.Unmarshal(resp.Body, &detail)
	
	desc := strings.ReplaceAll(compactText(detail.Album.Description), "\n", "")
	pdfLink := fetchQobuzPDFLink(input.Name, input.Artist)
	if pdfLink != "" {
		if desc != "" { return &metadata.AlbumInfoResponse{Description: pdfLink + "" + desc}, nil }
		return &metadata.AlbumInfoResponse{Description: pdfLink}, nil
	}
	return &metadata.AlbumInfoResponse{Description: desc}, nil
}

func (a *neteaseAgent) GetArtistBiography(input metadata.ArtistRequest) (*metadata.ArtistBiographyResponse, error) {
	artistID, _, _ := resolveID(cleanSearchTerm(input.Name), 100)
	if artistID == 0 { return nil, nil }
	resp, _ := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: fmt.Sprintf("%s/v1/artist/%d", neteaseBaseURL, artistID), Headers: buildNeteaseHeaders(nil)})
	var detail struct { Artist struct { BriefDesc string `json:"briefDesc"` } `json:"artist"` }
	json.Unmarshal(resp.Body, &detail)
	return &metadata.ArtistBiographyResponse{Biography: strings.ReplaceAll(compactText(detail.Artist.BriefDesc), "\n", "<br>")}, nil
}

func (a *neteaseAgent) GetArtistImages(input metadata.ArtistRequest) (*metadata.ArtistImagesResponse, error) {
	_, pic, _ := resolveID(cleanSearchTerm(input.Name), 100)
	
	if pic != "" && getConfigBool("enable_write_artist_image", true) {
		artistDir := guessArtistPath(input.Name)
		if artistDir != "" {
			downloadImage(pic, filepath.Join(artistDir, "artist.jpg"))
		}
	}

	if pic == "" { return nil, nil }
	res := getConfigString("image_resolution", "1200")
	full := fmt.Sprintf("%s?param=%sy%s", strings.Replace(pic, "http://", "https://", 1), res, res)
	var size int32
	fmt.Sscanf(res, "%d", &size)
	return &metadata.ArtistImagesResponse{Images: []metadata.ImageInfo{{URL: full, Size: size}}}, nil
}

func (a *neteaseAgent) GetAlbumImages(input metadata.AlbumRequest) (*metadata.AlbumImagesResponse, error) {
	triggerAlbumPreload(input.Name, input.Artist)

	_, pic, _ := resolveID(fmt.Sprintf("%s %s", cleanSearchTerm(input.Name), cleanSearchTerm(input.Artist)), 10)
	if pic == "" { return nil, nil }
	res := getConfigString("image_resolution", "1200")
	full := fmt.Sprintf("%s?param=%sy%s", strings.Replace(pic, "http://", "https://", 1), res, res)
	var size int32
	fmt.Sscanf(res, "%d", &size)
	return &metadata.AlbumImagesResponse{Images: []metadata.ImageInfo{{URL: full, Size: size}}}, nil
}

func (a *neteaseAgent) GetSimilarArtists(input metadata.SimilarArtistsRequest) (*metadata.SimilarArtistsResponse, error) {
	artistID, _, _ := resolveID(cleanSearchTerm(input.Name), 100)
	if artistID == 0 { return nil, nil }
	
	payload := fmt.Sprintf("artistid=%d", artistID)

	headers := buildNeteaseHeaders(map[string]string{
		"Referer":      "https://music.163.com/",
		"Content-Type": "application/x-www-form-urlencoded",
	})
	
	resp, err := host.HTTPSend(host.HTTPRequest{
		Method:  "POST", 
		URL:     "https://music.163.com/api/discovery/simiArtist", 
		Headers: headers,
		Body:    []byte(payload),
	})
	
	if err != nil {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("[Netease API] 获取相似艺人请求失败: %v", err))
		return nil, nil
	}

	var sr struct {
		Code    int `json:"code"`
		Artists []struct {
			Id        int64  `json:"id"`
			Name      string `json:"name"`
			PicUrl    string `json:"picUrl"`
			Img1v1Url string `json:"img1v1Url"`
		} `json:"artists"`
	}

	if err := json.Unmarshal(resp.Body, &sr); err != nil {
		pdk.Log(pdk.LogInfo, "[Netease API] 相似艺人 JSON 解析失败")
		return nil, nil
	}

	if sr.Code != 200 {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("[Netease API] 相似艺人获取被拦截或无数据, Code: %d", sr.Code))
		return nil, nil
	}

	var res []metadata.ArtistRef
	for _, art := range sr.Artists {
		if art.Name != "" {
			res = append(res, metadata.ArtistRef{
				ID:   fmt.Sprintf("netease_art_%d", art.Id),
				Name: art.Name,
			})
			
			pic := art.Img1v1Url
			if pic == "" || pic == "None" { pic = art.PicUrl }
			if pic != "" {
				cacheKey := fmt.Sprintf("id_map:100:%s", strings.ToLower(art.Name))
				b, _ := json.Marshal(IDCacheData{ID: art.Id, Pic: pic}) 
				cacheSet(cacheKey, b)
			}
		}
	}
	
	pdk.Log(pdk.LogInfo, fmt.Sprintf("[Netease API] 成功获取并映射 %s 的相似艺人: %d 个", input.Name, len(res)))
	return &metadata.SimilarArtistsResponse{Artists: res}, nil
}

func (a *neteaseAgent) GetArtistURL(input metadata.ArtistRequest) (*metadata.ArtistURLResponse, error) {
	id, _, _ := resolveID(cleanSearchTerm(input.Name), 100)
	if id == 0 { return nil, nil }
	return &metadata.ArtistURLResponse{URL: fmt.Sprintf("https://music.163.com/#/artist?id=%d", id)}, nil
}

func (a *neteaseAgent) GetArtistTopSongs(input metadata.TopSongsRequest) (*metadata.TopSongsResponse, error) { return nil, nil }
