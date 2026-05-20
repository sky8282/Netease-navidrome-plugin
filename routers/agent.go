package routers

import (
	"os"
	"fmt"
	"regexp"
	"net/url"
	"strconv"
	"strings"
	"encoding/json"
	"path/filepath"

	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/metadata"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
)

const defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_3) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"

var rePDFLink = regexp.MustCompile(`href="([^"]+)"`)

type neteaseAgent struct{}

var (
	_ metadata.ArtistURLProvider       = (*neteaseAgent)(nil)
	_ metadata.ArtistBiographyProvider = (*neteaseAgent)(nil)
	_ metadata.ArtistImagesProvider    = (*neteaseAgent)(nil)
	_ metadata.SimilarArtistsProvider  = (*neteaseAgent)(nil)
	_ metadata.ArtistTopSongsProvider  = (*neteaseAgent)(nil)
	_ metadata.AlbumImagesProvider     = (*neteaseAgent)(nil)
	_ metadata.AlbumInfoProvider       = (*neteaseAgent)(nil)
	_ scrobbler.Scrobbler              = (*neteaseAgent)(nil)
)

func Init() {
	agent := &neteaseAgent{}
	metadata.Register(agent)
	scrobbler.Register(agent)

	InitQueue()
}

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

func getNavidromeUser() string { return getConfigString("navidrome_user", "admin") }

func (a *neteaseAgent) GetAlbumInfo(input metadata.AlbumRequest) (*metadata.AlbumInfoResponse, error) {
	focusKey := cleanSearchTerm(input.Name) + "|||" + cleanSearchTerm(input.Artist)
	host.KVStoreSet("global_focus_album", []byte(focusKey))

	searchTerm := fmt.Sprintf("%s %s", cleanSearchTerm(input.Name), cleanSearchTerm(input.Artist))
	albumID, _, _ := resolveID(searchTerm, 10)

	if albumID == 0 {
		return nil, nil
	}

	respRaw, err := host.HTTPSend(host.HTTPRequest{
		Method:  "GET",
		URL:     fmt.Sprintf("https://music.163.com/api/v1/album/%d", albumID),
		Headers: buildNeteaseHeaders(nil),
	})

	if err == nil && respRaw != nil && respRaw.StatusCode == 200 {
		var minimalResp struct {
			Album struct {
				Description string `json:"description"`
			} `json:"album"`
		}
		if json.Unmarshal(respRaw.Body, &minimalResp) == nil {
			desc := compactText(minimalResp.Album.Description)
			if desc == "" {
				return nil, nil
			}
			descHtml := strings.ReplaceAll(desc, "\n", "<br>")
			return &metadata.AlbumInfoResponse{Description: descHtml}, nil
		}
	}
	return nil, nil
}

func (a *neteaseAgent) IsAuthorized(_ scrobbler.IsAuthorizedRequest) (bool, error) {
	return true, nil
}

func checkAndAutoDownload(trackTitle, trackArtist, trackAlbum, relPath string) {
	if !getConfigBool("enable_auto_download_on_play", false) {
		return
	}
	if relPath == "" {
		return
	}

	absPath := resolveFromRelativePath(relPath)
	if absPath == "" {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("⚠️ 物理路径推导失败，跳过: %s", relPath))
		return
	}

	if !TryLockTrack(absPath) {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("🛡️ 15分钟内已处理过该曲目，拦截重复触发: %s", trackTitle))
		return
	}

	albumDir := getCleanAlbumDir(absPath)
	if albumDir != "" {
		coverPath := filepath.Join(albumDir, "cover.jpg")
		
		coverExists := false
		if stat, err := os.Stat(coverPath); err == nil && stat.Size() > 1024 {
			coverExists = true
		}

		if !coverExists {
			pdk.Log(pdk.LogInfo, fmt.Sprintf("🚨 播放触发：检测到 [%s] 封面缺失，开始拉取...", trackAlbum))

			albumData, _ := getOrFetchCompleteAlbumData(trackAlbum, trackArtist, albumDir)
			if albumData.AlbumID > 0 {
				if getConfigBool("enable_write_cover_image", true) {
					downloadImage(albumData.PicURL, coverPath)
				}
				if albumData.ArtistPicURL != "" {
					if getConfigBool("enable_write_artist_image", true) {
						downloadImage(albumData.ArtistPicURL, filepath.Join(filepath.Dir(albumDir), "artist.jpg"))
					}
					if getConfigBool("enable_write_global_artist_image", true) {
						saveGlobalArtistImage(trackArtist, albumData.ArtistPicURL)
					}
				}

				if getConfigBool("enable_write_pdf", true) || getConfigBool("enable_qobuz_pdf", true) {
					pdfLinkHTML := fetchQobuzPDFLink(trackAlbum, trackArtist)
					if pdfLinkHTML != "" {
						albumData.PDFLink = pdfLinkHTML
						saveLocalAlbumData(albumDir, albumData) 
						
						match := rePDFLink.FindStringSubmatch(pdfLinkHTML)
						if len(match) > 1 {
							pdfURL := match[1]
							pdfPath := filepath.Join(albumDir, "booklet.pdf")
							if stat, err := os.Stat(pdfPath); err != nil || stat.Size() < 1024 {
								pdk.Log(pdk.LogInfo, fmt.Sprintf("📄 提取到 PDF 直链，正在静默下载小册子..."))
								pdfResp, errDl := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: pdfURL, Headers: map[string]string{"User-Agent": defaultUserAgent}})
								if errDl == nil && pdfResp.StatusCode == 200 {
									os.WriteFile(pdfPath, pdfResp.Body, 0666)
									pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ PDF 成功保存至: %s", pdfPath))
								}
							}
						}
					}
				}
				pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ [%s] 专辑信息获取完成", trackAlbum))
			} else {
				pdk.Log(pdk.LogError, fmt.Sprintf("❌ 无法从网易云获取 [%s] 的有效数据", trackAlbum))
			}
		}

		if getConfigBool("enable_write_lyrics", true) {
			ext := filepath.Ext(absPath)
			baseName := strings.TrimSuffix(filepath.Base(absPath), ext)
			lrcPath := filepath.Join(albumDir, baseName+".lrc")
			
			if stat, err := os.Stat(lrcPath); err != nil || stat.Size() == 0 {
				pdk.Log(pdk.LogInfo, fmt.Sprintf("🎤 检查到 [%s] 本地歌词缺失，正在后台静默拉取...", trackTitle))
				fetchAndWriteLocalLyrics(trackTitle, trackArtist, trackAlbum, absPath, 0)
			}
		}
	}
}

func (a *neteaseAgent) NowPlaying(req scrobbler.NowPlayingRequest) error {
	checkAndAutoDownload(req.Track.Title, req.Track.Artist, req.Track.Album, req.Track.Path)
	return nil
}

func (a *neteaseAgent) Scrobble(req scrobbler.ScrobbleRequest) error {
	checkAndAutoDownload(req.Track.Title, req.Track.Artist, req.Track.Album, req.Track.Path)
	return nil
}

func (a *neteaseAgent) GetArtistBiography(input metadata.ArtistRequest) (*metadata.ArtistBiographyResponse, error) {
	focusKey := cleanSearchTerm(input.Name)
	host.KVStoreSet("global_focus_artist", []byte(focusKey))

	artistID, _, _ := resolveID(cleanSearchTerm(input.Name), 100)
	if artistID == 0 {
		return nil, nil
	}

	resp, err := host.HTTPSend(host.HTTPRequest{
		Method:  "GET",
		URL:     fmt.Sprintf("https://music.163.com/api/v1/artist/%d", artistID),
		Headers: buildNeteaseHeaders(nil),
	})

	if err == nil && resp != nil && resp.StatusCode == 200 {
		var detail struct {
			Artist struct {
				BriefDesc string `json:"briefDesc"`
			} `json:"artist"`
		}
		if json.Unmarshal(resp.Body, &detail) == nil {
			desc := compactText(detail.Artist.BriefDesc)
			if desc == "" {
				return nil, nil
			}
			descHtml := strings.ReplaceAll(desc, "\n", "<br>")
			return &metadata.ArtistBiographyResponse{Biography: descHtml}, nil
		}
	}
	return nil, nil
}

func (a *neteaseAgent) GetAlbumImages(input metadata.AlbumRequest) (*metadata.AlbumImagesResponse, error) {
	focusKey := cleanSearchTerm(input.Name) + "|||" + cleanSearchTerm(input.Artist)
	currentFocus, ok, _ := host.KVStoreGet("global_focus_album")

	if ok && string(currentFocus) == focusKey {
		EnqueueAlbumTask(input.Name, input.Artist)
	}

	_, pic, _ := resolveID(fmt.Sprintf("%s %s", cleanSearchTerm(input.Name), cleanSearchTerm(input.Artist)), 10)
	if pic == "" {
		return nil, nil
	}
	res := getConfigString("image_resolution", "1200")
	full := fmt.Sprintf("%s?param=%sy%s", strings.Replace(pic, "http://", "https://", 1), res, res)
	
	sizeInt, _ := strconv.Atoi(res)
	size := int32(sizeInt)
	
	return &metadata.AlbumImagesResponse{Images: []metadata.ImageInfo{{URL: full, Size: size}}}, nil
}

func (a *neteaseAgent) GetArtistImages(input metadata.ArtistRequest) (*metadata.ArtistImagesResponse, error) {
	focusKey := cleanSearchTerm(input.Name)
	currentFocus, ok, _ := host.KVStoreGet("global_focus_artist")

	if ok && string(currentFocus) == focusKey {
		EnqueueArtistTask(input.Name)
	}

	_, pic, _ := resolveID(cleanSearchTerm(input.Name), 100)
	if pic == "" {
		return nil, nil
	}
	res := getConfigString("image_resolution", "1200")
	full := fmt.Sprintf("%s?param=%sy%s", strings.Replace(pic, "http://", "https://", 1), res, res)
	
	sizeInt, _ := strconv.Atoi(res)
	size := int32(sizeInt)

	return &metadata.ArtistImagesResponse{Images: []metadata.ImageInfo{{URL: full, Size: size}}}, nil
}

func (a *neteaseAgent) GetSimilarArtists(input metadata.SimilarArtistsRequest) (*metadata.SimilarArtistsResponse, error) {
	cleanName := cleanSearchTerm(input.Name)
	cleanName = strings.TrimPrefix(cleanName, "similar-")

	artistID, _, _ := resolveID(cleanName, 100)
	if artistID == 0 {
		return nil, nil
	}

	payload := fmt.Sprintf("artistid=%d", artistID)
	simiBody, err := host.HTTPSend(host.HTTPRequest{
		Method:  "POST",
		URL:     "https://music.163.com/api/discovery/simiArtist",
		Headers: buildNeteaseHeaders(map[string]string{
			"Referer":      "https://music.163.com/",
			"Content-Type": "application/x-www-form-urlencoded",
		}),
		Body:    []byte(payload),
	})

	if err != nil || simiBody == nil {
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

	if err := json.Unmarshal(simiBody.Body, &sr); err != nil {
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
			if pic == "" || pic == "None" {
				pic = art.PicUrl
			}
			if pic != "" {
				Storage.SaveIDMap(100, strings.ToLower(art.Name), art.Id, pic)
			}
		}
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
	_ = url.URL{}
	artistID, _, _ := resolveID(cleanSearchTerm(input.Name), 100)
	if artistID == 0 {
		return nil, nil
	}

	respRaw, err := host.HTTPSend(host.HTTPRequest{
		Method:  "GET",
		URL:     fmt.Sprintf("https://music.163.com/api/v1/artist/%d", artistID),
		Headers: buildNeteaseHeaders(nil),
	})

	if err != nil || respRaw == nil || respRaw.StatusCode != 200 {
		return nil, nil
	}

	var artResp struct {
		HotSongs []struct {
			Id   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"hotSongs"`
	}

	if err := json.Unmarshal(respRaw.Body, &artResp); err != nil {
		return nil, nil
	}

	var songs []metadata.SongRef
	for _, t := range artResp.HotSongs {
		if t.Name != "" {
			songs = append(songs, metadata.SongRef{
				ID:   fmt.Sprintf("netease_song_%d", t.Id),
				Name: t.Name,
			})
		}
	}
	return &metadata.TopSongsResponse{Songs: songs}, nil
}