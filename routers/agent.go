package routers

import (
	"os"
	"fmt"
	"strings"
	"net/url"
	"encoding/json"
	"path/filepath"
	
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/metadata"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
)

const defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_3) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"

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

func Init() {
	agent := &neteaseAgent{}
	metadata.Register(agent)
	lyrics.Register(agent)
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

func debugLog(msg string) {
	if getConfigBool("enable_debug_log", true) {
		pdk.Log(pdk.LogInfo, "ℹ️ [Netease Debug] "+msg)
	}
}

func (a *neteaseAgent) GetAlbumInfo(input metadata.AlbumRequest) (*metadata.AlbumInfoResponse, error) {
	albumDir := resolveAlbumDir(input.Name, input.Artist)

	localData, found := getOrFetchCompleteAlbumData(input.Name, input.Artist, albumDir)

	if found {
		desc := strings.ReplaceAll(localData.Description, "\n", "<br>")
		return &metadata.AlbumInfoResponse{Description: desc}, nil
	}

	albumID, _, _ := resolveID(fmt.Sprintf("%s %s", cleanSearchTerm(input.Name), cleanSearchTerm(input.Artist)), 10)
	if albumID == 0 {
		return nil, nil
	}

	albumBody, errAlbum := smartAlbumDetailAPI(albumID)
	if errAlbum != nil {
		return nil, nil
	}
	var detail struct {
		Album struct {
			Description string `json:"description"`
		} `json:"album"`
	}
	json.Unmarshal(albumBody, &detail)

	desc := strings.ReplaceAll(compactText(detail.Album.Description), "\n", "")
	return &metadata.AlbumInfoResponse{Description: desc}, nil
}

func (a *neteaseAgent) IsAuthorized(_ scrobbler.IsAuthorizedRequest) (bool, error) {
	return true, nil
}

func (a *neteaseAgent) NowPlaying(req scrobbler.NowPlayingRequest) error {
	finalArtist, abs := getTrackArtistAndDir(req.Username, req.Track.ID, req.Track.Artist, req.Track.Path)
	if abs != "" {
		albumDir := getCleanAlbumDir(abs)
		
		Storage.SaveAlbumPath(req.Track.Album, finalArtist, albumDir)
		Storage.SaveArtistPath(finalArtist, filepath.Dir(albumDir))

		fetchMetadataAndTag(abs, req.Track.Title, finalArtist, req.Track.Album)
	}
	return nil
}

func (a *neteaseAgent) Scrobble(req scrobbler.ScrobbleRequest) error {
	finalArtist, abs := getTrackArtistAndDir(req.Username, req.Track.ID, req.Track.Artist, req.Track.Path)
	if abs != "" {
		albumDir := getCleanAlbumDir(abs)
		
		Storage.SaveAlbumPath(req.Track.Album, finalArtist, albumDir)
		Storage.SaveArtistPath(finalArtist, filepath.Dir(albumDir))

		fetchMetadataAndTag(abs, req.Track.Title, finalArtist, req.Track.Album)
	}
	return nil
}

func (a *neteaseAgent) GetLyrics(input lyrics.GetLyricsRequest) (lyrics.GetLyricsResponse, error) {
	if !getConfigBool("enable_lyrics", true) {
		return lyrics.GetLyricsResponse{}, nil
	}
	finalArtist, abs := getTrackArtistAndDir(getNavidromeUser(), input.Track.ID, input.Track.Artist, input.Track.Path)
	if abs != "" {
		fetchMetadataAndTag(abs, input.Track.Title, finalArtist, input.Track.Album)
	}

	lyricText := fetchAndWriteLocalLyrics(input.Track.Title, finalArtist, input.Track.Album, abs, 0)

	if lyricText == "" {
		return lyrics.GetLyricsResponse{}, nil
	}
	return lyrics.GetLyricsResponse{Lyrics: []lyrics.LyricsText{{Text: lyricText}}}, nil
}

func (a *neteaseAgent) GetArtistBiography(input metadata.ArtistRequest) (*metadata.ArtistBiographyResponse, error) {
	artistID, _, _ := resolveID(cleanSearchTerm(input.Name), 100)
	if artistID == 0 {
		return nil, nil
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("🌐 获取艺人描述 ID: %d", artistID))

	resp, _ := host.HTTPSend(host.HTTPRequest{
		Method:  "GET",
		URL:     fmt.Sprintf("https://music.163.com/api/v1/artist/%d", artistID),
		Headers: buildNeteaseHeaders(nil),
	})
	var detail struct {
		Artist struct {
			BriefDesc string `json:"briefDesc"`
		} `json:"artist"`
	}
	if resp != nil && resp.StatusCode == 200 {
		json.Unmarshal(resp.Body, &detail)
		desc := strings.ReplaceAll(compactText(detail.Artist.BriefDesc), "\n", "<br>")
		return &metadata.ArtistBiographyResponse{Biography: desc}, nil
	}
	return nil, nil
}

func (a *neteaseAgent) GetAlbumImages(input metadata.AlbumRequest) (*metadata.AlbumImagesResponse, error) {
	albumDir := resolveAlbumDir(input.Name, input.Artist)
	if albumDir != "" {
		coverPath := filepath.Join(albumDir, "cover.jpg")
		if stat, err := os.Stat(coverPath); err == nil && stat.Size() > 1024 {
			pdk.Log(pdk.LogInfo, fmt.Sprintf("🛑 本地封面已存在，直接读取 (%s)", coverPath))
			return nil, nil
		}
	}

	EnqueueAlbumTask(input.Name, input.Artist)

	_, pic, _ := resolveID(fmt.Sprintf("%s %s", cleanSearchTerm(input.Name), cleanSearchTerm(input.Artist)), 10)
	if pic == "" {
		return nil, nil
	}
	res := getConfigString("image_resolution", "1200")
	full := fmt.Sprintf("%s?param=%sy%s", strings.Replace(pic, "http://", "https://", 1), res, res)
	var size int32
	fmt.Sscanf(res, "%d", &size)
	return &metadata.AlbumImagesResponse{Images: []metadata.ImageInfo{{URL: full, Size: size}}}, nil
}

func (a *neteaseAgent) GetArtistImages(input metadata.ArtistRequest) (*metadata.ArtistImagesResponse, error) {
	artistDir := resolveArtistDir(input.Name)
	if artistDir != "" {
		artistImgPath := filepath.Join(artistDir, "artist.jpg")
		if stat, err := os.Stat(artistImgPath); err == nil && stat.Size() > 1024 {
			pdk.Log(pdk.LogInfo, fmt.Sprintf("🛑 本地头像已存在，直接读取 (%s)", artistImgPath))
			return nil, nil
		}
	}

	_, pic, _ := resolveID(cleanSearchTerm(input.Name), 100)

	if pic != "" {
		if artistDir != "" && getConfigBool("enable_write_artist_image", true) {
			downloadImage(pic, filepath.Join(artistDir, "artist.jpg"))
		}
		if getConfigBool("enable_write_global_artist_image", true) {
			saveGlobalArtistImage(input.Name, pic)
		}
	}

	if pic == "" {
		return nil, nil
	}
	res := getConfigString("image_resolution", "1200")
	full := fmt.Sprintf("%s?param=%sy%s", strings.Replace(pic, "http://", "https://", 1), res, res)
	var size int32
	fmt.Sscanf(res, "%d", &size)
	return &metadata.ArtistImagesResponse{Images: []metadata.ImageInfo{{URL: full, Size: size}}}, nil
}

func (a *neteaseAgent) GetSimilarArtists(input metadata.SimilarArtistsRequest) (*metadata.SimilarArtistsResponse, error) {
	cleanName := cleanSearchTerm(input.Name)
	cleanName = strings.TrimPrefix(cleanName, "similar-")
	
	artistID, _, _ := resolveID(cleanName, 100)
	if artistID == 0 {
		return nil, nil
	}

	simiBody, errSimi := smartSimiArtistAPI(artistID)
	if errSimi != nil || len(simiBody) == 0 {
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

	if err := json.Unmarshal(simiBody, &sr); err != nil {
		pdk.Log(pdk.LogError, "❌ 相似艺人 JSON 解析失败: "+err.Error())
		return nil, nil
	}

	if sr.Code != 200 {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("🛑 相似艺人获取被拦截或无数据, Code: %d", sr.Code))
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

	pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ 成功获取并映射 %s 的相似艺人: %d 个", cleanName, len(res)))
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

	pdk.Log(pdk.LogInfo, fmt.Sprintf("⏳ 获取歌手 [%s] 的热门歌曲 (ID: %d)...", input.Name, artistID))

	respRaw, err := host.HTTPSend(host.HTTPRequest{
		Method:  "GET",
		URL:     fmt.Sprintf("https://music.163.com/api/v1/artist/%d", artistID),
		Headers: buildNeteaseHeaders(nil),
	})

	if err != nil || respRaw == nil || respRaw.StatusCode != 200 {
		pdk.Log(pdk.LogError, "❌ 热门单曲列表获取失败")
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

	pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ 成功获取歌手 [%s] 的热门歌曲: %d 首", input.Name, len(songs)))
	return &metadata.TopSongsResponse{Songs: songs}, nil
}
