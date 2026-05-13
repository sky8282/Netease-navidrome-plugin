package routers

import (
	"io"
	"os"
	"fmt"
	"sync"
	"time"
	"regexp"
	"strings"
	"path/filepath"

	"github.com/bogem/id3v2/v2"
	"github.com/go-flac/go-flac"
	"github.com/go-flac/flacvorbis"
	"github.com/Sorrow446/go-mp4tag"
	"github.com/go-flac/flacpicture"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

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
		return fmt.Errorf("🚨 未找到真实的 fLaC 标识，跳过修复")
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
			pdk.Log(pdk.LogInfo, fmt.Sprintf("⛔ 处理 %s 时发生致命错误，已跳过: %v", filename, r))
		}
	}()

	artistStr := strings.Join(song.Artists, "/")

	switch ext {
	case ".mp3":
		tag, err := id3v2.Open(absPath, id3v2.Options{Parse: true})
		if err != nil {
			pdk.Log(pdk.LogInfo, fmt.Sprintf("⛔ 无法打开 MP3: %s, err: %v", filename, err))
			return false
		}
		defer tag.Close()
		tag.SetDefaultEncoding(id3v2.EncodingUTF8)

		changed := false

		if tag.Artist() == "" && artistStr != "" {
			tag.SetArtist(artistStr)
			changed = true
		}
		if tag.Album() == "" && album.AlbumName != "" {
			tag.SetAlbum(album.AlbumName)
			changed = true
		}
		if tag.Title() == "" && song.Name != "" {
			tag.SetTitle(song.Name)
			changed = true
		}
		if tag.Year() == "" && year != "" {
			tag.SetYear(year)
			changed = true
		}

		if len(tag.GetFrames("TIT1")) == 0 && song.Work != "" {
			tag.AddTextFrame("TIT1", id3v2.EncodingUTF8, song.Work)
			changed = true
		}

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
				if _, ok := frame.(id3v2.CommentFrame); ok {
					hasComm = true
				}
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
				if _, ok := frame.(id3v2.PictureFrame); ok {
					hasPic = true
				}
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
			if err := tag.Save(); err != nil {
				return false
			}
			pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ 成功写入 MP3 标签: %s", filename))
			return true
		}
		return true

	case ".flac":
		f, err := flac.ParseFile(absPath)
		if err != nil {
			if strings.Contains(err.Error(), "fLaC head incorrect") {
				pdk.Log(pdk.LogInfo, fmt.Sprintf("⚠️ 尝试修复 FLAC 元数据: %s", filename))
				if fixErr := cleanFlacFile(absPath); fixErr == nil {
					f, err = flac.ParseFile(absPath)
				}
			}
			if err != nil {
				return false
			}
		}

		var cmt *flacvorbis.MetaDataBlockVorbisComment
		for _, meta := range f.Meta {
			if meta.Type == flac.VorbisComment {
				cmt, _ = flacvorbis.ParseFromMetaDataBlock(*meta)
				break
			}
		}
		if cmt == nil {
			cmt = flacvorbis.New()
		}

		getFlacLen := func(key string) int { v, _ := cmt.Get(key); return len(v) }
		changed := false

		if getFlacLen("TITLE") == 0 && song.Name != "" {
			cmt.Add("TITLE", song.Name)
			changed = true
		}

		if getFlacLen("WORK") == 0 && song.Work != "" {
			cmt.Add("WORK", song.Work)
			changed = true
		}
		if getFlacLen("GROUPING") == 0 && song.Work != "" {
			cmt.Add("GROUPING", song.Work)
			changed = true
		}

		if getFlacLen("ARTIST") == 0 && len(song.Artists) > 0 {
			for _, a := range song.Artists {
				cmt.Add("ARTIST", a)
			}
			cmt.Add("ALBUMARTIST", artistStr)
			changed = true
		}
		if getFlacLen("ALBUM") == 0 && album.AlbumName != "" {
			cmt.Add("ALBUM", album.AlbumName)
			changed = true
		}
		if getFlacLen("DATE") == 0 && year != "" {
			cmt.Add("DATE", year)
			changed = true
		}
		if getFlacLen("TRACKNUMBER") == 0 && song.TrackNum > 0 {
			cmt.Add("TRACKNUMBER", fmt.Sprintf("%d", song.TrackNum))
			changed = true
		}
		if getFlacLen("DISCNUMBER") == 0 && song.DiscNum > 0 {
			cmt.Add("DISCNUMBER", fmt.Sprintf("%d", song.DiscNum))
			changed = true
		}
		if getFlacLen("ORGANIZATION") == 0 && getFlacLen("LABEL") == 0 && album.Company != "" {
			cmt.Add("ORGANIZATION", album.Company)
			cmt.Add("LABEL", album.Company)
			changed = true
		}
		if getFlacLen("ISRC") == 0 && song.ISRC != "" {
			cmt.Add("ISRC", song.ISRC)
			changed = true
		}
		if getFlacLen("GENRE") == 0 && song.Genre != "" {
			cmt.Add("GENRE", song.Genre)
			changed = true
		}
		if getFlacLen("COMMENT") == 0 && comment != "" {
			cmt.Add("COMMENT", comment)
			changed = true
		}
		if getFlacLen("LYRICS") == 0 && lyric != "" {
			cmt.Add("LYRICS", lyric)
			changed = true
		}

		hasPic := false
		var newMeta []*flac.MetaDataBlock
		for _, meta := range f.Meta {
			if meta.Type != flac.VorbisComment {
				if meta.Type == flac.Picture {
					hasPic = true
				}
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
			if err := os.Rename(tempPath, absPath); err != nil {
				return false
			}
			pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ 成功写入 FLAC 标签: %s", filename))
			return true
		}
		return true

	case ".m4a", ".alac", ".aac":
		mp4, err := mp4tag.Open(absPath)
		if err != nil {
			return false
		}
		defer mp4.Close()
		tags, err := mp4.Read()
		if err != nil {
			tags = &mp4tag.MP4Tags{}
		}

		changed := false

		if tags.Title == "" && song.Name != "" {
			tags.Title = song.Name
			changed = true
		}
		if tags.Artist == "" && artistStr != "" {
			tags.Artist = artistStr
			changed = true
		}
		if tags.AlbumArtist == "" && artistStr != "" {
			tags.AlbumArtist = artistStr
			changed = true
		}
		if tags.Album == "" && album.AlbumName != "" {
			tags.Album = album.AlbumName
			changed = true
		}
		if tags.Date == "" && year != "" {
			tags.Date = year
			changed = true
		}
		if tags.TrackNumber == 0 && song.TrackNum > 0 {
			tags.TrackNumber = int16(song.TrackNum)
			changed = true
		}
		if tags.DiscNumber == 0 && song.DiscNum > 0 {
			tags.DiscNumber = int16(song.DiscNum)
			changed = true
		}
		if tags.CustomGenre == "" && song.Genre != "" {
			tags.CustomGenre = song.Genre
			changed = true
		}

		if tags.Custom == nil {
			tags.Custom = make(map[string]string)
		}
		if _, exists := tags.Custom["label"]; !exists && album.Company != "" {
			tags.Custom["label"] = album.Company
			changed = true
		}
		if _, exists := tags.Custom["ISRC"]; !exists && song.ISRC != "" {
			tags.Custom["ISRC"] = song.ISRC
			changed = true
		}

		if _, exists := tags.Custom["WORK"]; !exists && song.Work != "" {
			tags.Custom["WORK"] = song.Work
			changed = true
		}
		if _, exists := tags.Custom["GROUPING"]; !exists && song.Work != "" {
			tags.Custom["GROUPING"] = song.Work
			changed = true
		}

		if tags.Comment == "" && comment != "" {
			tags.Comment = comment
			changed = true
		}
		if tags.Lyrics == "" && lyric != "" {
			tags.Lyrics = lyric
			changed = true
		}

		if len(tags.Pictures) == 0 && len(picData) > 0 {
			tags.Pictures = []*mp4tag.MP4Picture{{Data: picData}}
			changed = true
		}

		if changed {
			if err := mp4.Write(tags, []string{}); err != nil {
				return false
			}
			pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ 成功写入 M4A 标签: %s", filename))
			return true
		}
		return true
	}
	return false
}

var albumScrapeLocks sync.Map

func triggerAlbumPreload(albumName, artistName string) {
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

	pdk.Log(pdk.LogInfo, fmt.Sprintf("⏳ 验证/生成 元数据 JSON: %s", albumDir))

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
	pdk.Log(pdk.LogInfo, "✅ 专辑预加载执行完毕")
}

func fetchMetadataAndTag(absPath, title, artist, originalAlbum string) {
	if !getConfigBool("enable_write_metadata", true) {
		return
	}
	ext := strings.ToLower(filepath.Ext(absPath))
	if ext == ".wav" {
		return
	}

	lockKey := fmt.Sprintf("track_lock:%s", absPath)
	if lockData, ok, _ := host.KVStoreGet(lockKey); ok {
		var ts int64
		fmt.Sscanf(string(lockData), "%d", &ts)
		if time.Now().Unix()-ts < 15 {
			return
		}
	}
	host.KVStoreSet(lockKey, []byte(fmt.Sprintf("%d", time.Now().Unix())))

	albumDir := getCleanAlbumDir(absPath)
	fileName := filepath.Base(absPath)

	if isTrackProcessed(albumDir, fileName) {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ 跳过 (此曲目已完成写入): %s", fileName))
		return
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("🚀 正在处理并写入曲目元数据: %s", fileName))

	albumData, found := getOrFetchCompleteAlbumData(originalAlbum, artist, albumDir)
	if !found || albumData.AlbumID == 0 {
		return
	}

	if getConfigBool("enable_write_cover_image", true) && albumData.PicURL != "" {
		downloadImage(albumData.PicURL, filepath.Join(albumDir, "cover.jpg"))
	}
	
	if albumData.ArtistPicURL != "" {
		if getConfigBool("enable_write_artist_image", true) {
			downloadImage(albumData.ArtistPicURL, filepath.Join(filepath.Dir(albumDir), "artist.jpg"))
		}
		if getConfigBool("enable_write_global_artist_image", true) {
			saveGlobalArtistImage(artist, albumData.ArtistPicURL)
		}
	}

	downloadQobuzPDFToDisk(originalAlbum, artist, albumDir)

	matchedSong, foundSong := matchLocalFileToNeteaseSong(fileName, albumData.Songs)
	if !foundSong {
		matchedSong = SongData{Artists: []string{artist}}
		work, title := extractWorkAndTitle(strings.TrimSuffix(fileName, ext))
		matchedSong.Name = title
		matchedSong.Work = work
	}

	lyricText := fetchAndWriteLocalLyrics(matchedSong.Name, artist, originalAlbum, absPath, matchedSong.ID)

	var picData []byte
	if getConfigBool("enable_write_cover_image", true) {
		picData, _ = os.ReadFile(filepath.Join(albumDir, "cover.jpg"))
	}

	finalComment := albumData.Description

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
	ext := filepath.Ext(filename)
	nameWithoutExt := strings.TrimSuffix(filename, ext)
	reNum := regexp.MustCompile(`^\s*0*(\d+)[\.\-\s]*`)
	match := reNum.FindStringSubmatch(nameWithoutExt)
	var fileTrackNum int
	if len(match) > 1 {
		fmt.Sscanf(match[1], "%d", &fileTrackNum)
	}

	cleanFileName := cleanSongTitle(nameWithoutExt)
	cleanFileNameLower := strings.ToLower(cleanFileName)

	for _, s := range songs {
		apiCleanName := strings.ToLower(cleanSongTitle(s.Name))
		if cleanFileNameLower == apiCleanName {
			return s, true
		}
	}

	for _, s := range songs {
		apiCleanName := cleanSongTitle(s.Name)
		if fuzzyMatch(cleanFileName, apiCleanName) {
			return s, true
		}
	}

	if fileTrackNum > 0 {
		continuousTrackNum := 0
		for _, s := range songs {
			continuousTrackNum++
			if s.TrackNum == fileTrackNum || continuousTrackNum == fileTrackNum {
				return s, true
			}
		}
	}

	return SongData{}, false
}