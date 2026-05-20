package routers

import (
	"os"
	"fmt"
	"errors"
	"regexp"
	"net/url"
	"strings"
	"path/filepath"
	"encoding/json"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

var errWalkStop = errors.New("stop walk")

type subsonicAlbumResponse struct {
	SubsonicResponse struct {
		Album struct {
			Song []struct {
				ID          string `json:"id"`
				Path        string `json:"path"`
				Artist      string `json:"artist"`
				AlbumArtist string `json:"albumArtist"`
				Suffix      string `json:"suffix"`
				Size        int64  `json:"size"`
			} `json:"song"`
		} `json:"album"`
	} `json:"subsonic-response"`
}

type subsonicSongResponse struct {
	SubsonicResponse struct {
		Song struct {
			Path        string `json:"path"`
			Suffix      string `json:"suffix"`
			Size        int64  `json:"size"`
			Artist      string `json:"artist"`
			AlbumArtist string `json:"albumArtist"`
		} `json:"song"`
	} `json:"subsonic-response"`
}

func getCleanAlbumDir(absPath string) string {
	albDir := filepath.Dir(absPath)
	base := strings.ToLower(filepath.Base(albDir))
	if reDiscFolder.MatchString(base) || base == "cd" || base == "disc" || base == "disk" {
		return filepath.Dir(albDir)
	}
	return albDir
}

func getBaseMusicDir() string {
	if cfgDir := getConfigString("music_folder", ""); cfgDir != "" {
		return cfgDir
	}

	libraries, err := host.LibraryGetAllLibraries()
	if err == nil && len(libraries) > 0 {
		for _, lib := range libraries {
			root := lib.MountPoint
			if root == "" {
				root = lib.Path
			}
			if root != "" {
				return root
			}
		}
	}
	return ""
}

func saveGlobalArtistImage(artistName string, picURL string) {
	if !getConfigBool("enable_write_global_artist_image", true) || picURL == "" || artistName == "" {
		return
	}
	baseDir := getBaseMusicDir()
	if baseDir == "" {
		return
	}

	artistFolder := filepath.Join(baseDir, "artist")
	os.MkdirAll(artistFolder, 0755)

	safeArtistName := strings.ReplaceAll(strings.ReplaceAll(artistName, "/", "_"), "\\", "_")
	savePath := filepath.Join(artistFolder, safeArtistName+".jpg")

	downloadImage(picURL, savePath)
}

func cleanArtistName(artist string) string {
	if artist == "[Unknown Artist]" || artist == "Unknown Artist" || artist == "Unknown" {
		return ""
	}
	return artist
}

func getAlbumDirAndArtistFromID(username, albumID string) (string, string) {
	if albumID == "" {
		return "", ""
	}
	if username == "" {
		username = getNavidromeUser()
	}
	jsonStr, err := host.SubsonicAPICall("getAlbum?id=" + albumID + "&u=" + username + "&f=json&v=1.16.0")
	if err != nil {
		return "", ""
	}
	var resp subsonicAlbumResponse
	json.Unmarshal([]byte(jsonStr), &resp)

	if len(resp.SubsonicResponse.Album.Song) > 0 {
		song := resp.SubsonicResponse.Album.Song[0]

		if song.Path == "" || song.Size == 0 {
			if detail, err := getSongDetailsFromSubsonic(username, song.ID); err == nil && detail != nil {
				if detail.SubsonicResponse.Song.Path != "" {
					song.Path = detail.SubsonicResponse.Song.Path
					song.Suffix = detail.SubsonicResponse.Song.Suffix
					song.Size = detail.SubsonicResponse.Song.Size
				}
			}
		}

		art := cleanArtistName(song.AlbumArtist)
		if art == "" {
			art = cleanArtistName(song.Artist)
		}

		abs, _ := resolveAbsolutePath(song.Path, song.Suffix, song.Size)
		if abs == "" {
			abs = resolveFromRelativePath(song.Path)
		}

		if abs != "" {
			if _, err := os.Stat(abs); err != nil {
				return "", ""
			}
			if realAbs, err := filepath.EvalSymlinks(abs); err == nil && realAbs != "" {
				abs = realAbs
			}
			return getCleanAlbumDir(abs), art
		}
	}
	return "", ""
}

func findAlbumDirViaSubsonicAPI(username, albumName, artistName string) (string, string) {
	if albumName == "" {
		return "", ""
	}
	if username == "" {
		username = getNavidromeUser()
	}
	searchTerm := cleanSearchTerm(albumName)
	if artistName != "" {
		searchTerm += " " + cleanSearchTerm(artistName)
	}
	query := url.QueryEscape(strings.TrimSpace(searchTerm))

	jsonStr, err := host.SubsonicAPICall(fmt.Sprintf("search3?query=%s&albumCount=50&u=%s&f=json&v=1.16.0", query, username))
	if err != nil {
		return "", ""
	}

	var resp struct {
		SubsonicResponse struct {
			SearchResult3 struct {
				Album []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"album"`
			} `json:"searchResult3"`
		} `json:"subsonic-response"`
	}
	json.Unmarshal([]byte(jsonStr), &resp)

	for _, alb := range resp.SubsonicResponse.SearchResult3.Album {
		if fuzzyMatch(alb.Name, albumName) {
			dir, art := getAlbumDirAndArtistFromID(username, alb.ID)
			if dir != "" {
				return dir, art
			}
		}
	}
	return "", ""
}

func resolveAlbumDirWithUser(username, albumName, artistName string) string {
	finalArtist := cleanArtistName(artistName)

	if dir, ok := Storage.GetAlbumPath(albumName, finalArtist); ok {
		if stat, err := os.Stat(dir); err == nil && stat.IsDir() {
			return dir
		}
	}

	if username == "" {
		username = getNavidromeUser()
	}

	dir, _ := findAlbumDirViaSubsonicAPI(username, albumName, finalArtist)
	if dir != "" {
		Storage.SaveAlbumPath(albumName, finalArtist, dir)
		return dir
	}

	dir = guessAlbumPath(albumName, finalArtist)
	if dir != "" {
		Storage.SaveAlbumPath(albumName, finalArtist, dir)
		return dir
	}

	return ""
}

func resolveAlbumDir(albumName, artistName string) string {
	return resolveAlbumDirWithUser(getNavidromeUser(), albumName, artistName)
}

func resolveArtistDir(artistName string) string {
	finalArtist := cleanArtistName(artistName)
	if finalArtist == "" {
		return ""
	}

	if dir, ok := Storage.GetArtistPath(finalArtist); ok {
		if stat, err := os.Stat(dir); err == nil && stat.IsDir() {
			return dir
		}
	}

	dir := guessArtistPath(finalArtist)
	if dir != "" {
		Storage.SaveArtistPath(finalArtist, dir)
	}
	return dir
}

func guessAlbumPath(albumName, artistName string) string {
	libraries, err := host.LibraryGetAllLibraries()
	if err != nil {
		return ""
	}

	pureCore := func(s string) string {
		reBrackets := regexp.MustCompile(`(?i)\[.*?\]|\(.*?\)|\{.*?\}|（.*?）|【.*?】`)
		s = reBrackets.ReplaceAllString(s, "")
		reChars := regexp.MustCompile(`[^\p{L}\p{N}]+`)
		return strings.ToLower(reChars.ReplaceAllString(s, ""))
	}

	targetAlbumCore := pureCore(albumName)
	targetArtistCore := pureCore(artistName)

	if targetAlbumCore == "" {
		return ""
	}

	for _, lib := range libraries {
		root := lib.MountPoint
		if root == "" {
			root = lib.Path
		}
		if root == "" {
			continue
		}

		var currentDirs []string
		currentDirs = append(currentDirs, root)

		for depth := 0; depth < 4; depth++ {
			var nextDirs []string
			for _, dir := range currentDirs {
				entries, err := os.ReadDir(dir)
				if err != nil {
					continue
				}

				for _, entry := range entries {
					if !entry.IsDir() {
						continue
					}
					entryName := entry.Name()
					if strings.HasPrefix(entryName, ".") {
						continue
					}

					entryCore := pureCore(entryName)
					if entryCore == "" {
						nextDirs = append(nextDirs, filepath.Join(dir, entryName))
						continue
					}

					if entryCore == targetAlbumCore || strings.Contains(entryCore, targetAlbumCore) || strings.Contains(targetAlbumCore, entryCore) {
						parentNameCore := pureCore(filepath.Base(dir))
						if targetArtistCore == "" || parentNameCore == targetArtistCore || strings.Contains(parentNameCore, targetArtistCore) {
							return filepath.Join(dir, entryName)
						}
						return filepath.Join(dir, entryName)
					}

					if targetArtistCore != "" && (entryCore == targetArtistCore || strings.Contains(entryCore, targetArtistCore)) {
						artistFullPath := filepath.Join(dir, entryName)
						subEntries, subErr := os.ReadDir(artistFullPath)
						if subErr == nil {
							for _, sub := range subEntries {
								if sub.IsDir() {
									subCore := pureCore(sub.Name())
									if subCore != "" && (subCore == targetAlbumCore || strings.Contains(subCore, targetAlbumCore) || strings.Contains(targetAlbumCore, subCore)) {
										return filepath.Join(artistFullPath, sub.Name())
									}
								}
							}
						}
					}

					nextDirs = append(nextDirs, filepath.Join(dir, entryName))
				}
			}
			currentDirs = nextDirs
			if len(currentDirs) == 0 || len(currentDirs) > 50000 {
				break
			}
		}
	}
	return ""
}

func guessArtistPath(artistName string) string {
	libraries, err := host.LibraryGetAllLibraries()
	if err != nil {
		return ""
	}

	pureCore := func(s string) string {
		reBrackets := regexp.MustCompile(`(?i)\[.*?\]|\(.*?\)|\{.*?\}`)
		s = reBrackets.ReplaceAllString(s, "")
		reChars := regexp.MustCompile(`[^\p{L}\p{N}]+`)
		return strings.ToLower(reChars.ReplaceAllString(s, ""))
	}

	targetArtistCore := pureCore(artistName)
	if targetArtistCore == "" {
		return ""
	}

	for _, lib := range libraries {
		root := lib.MountPoint
		if root == "" {
			root = lib.Path
		}
		if root == "" {
			continue
		}

		if entries, err := os.ReadDir(root); err == nil {
			for _, entry := range entries {
				if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
					entryCore := pureCore(entry.Name())
					if entryCore != "" && (entryCore == targetArtistCore || strings.Contains(entryCore, targetArtistCore)) {
						return filepath.Join(root, entry.Name())
					}
				}
			}
		}
	}
	return ""
}

func getLocalAlbumsForArtistWithUser(username, artistName string) []string {
	var albums []string
	if username == "" {
		username = getNavidromeUser()
	}

	query := url.QueryEscape(artistName)
	jsonStr, err := host.SubsonicAPICall(fmt.Sprintf("search3?query=%s&albumCount=50&u=%s&f=json&v=1.16.0", query, username))
	if err == nil {
		var resp struct {
			SubsonicResponse struct {
				SearchResult3 struct {
					Album []struct {
						Name   string `json:"name"`
						Artist string `json:"artist"`
					} `json:"album"`
				} `json:"searchResult3"`
			} `json:"subsonic-response"`
		}
		if json.Unmarshal([]byte(jsonStr), &resp) == nil {
			for _, alb := range resp.SubsonicResponse.SearchResult3.Album {
				if fuzzyMatch(alb.Artist, artistName) {
					albums = append(albums, alb.Name)
				}
			}
		}
	}

	if len(albums) == 0 {
		artistDir := guessArtistPath(artistName)
		if artistDir != "" {
			if entries, err := os.ReadDir(artistDir); err == nil {
				for _, entry := range entries {
					if entry.IsDir() {
						albums = append(albums, entry.Name())
					}
				}
			}
		}
	}

	return albums
}

func getLocalAlbumsForArtist(artistName string) []string {
	return getLocalAlbumsForArtistWithUser(getNavidromeUser(), artistName)
}

func findAudioBySize(root, suffix string, size int64) (string, error) {
	if size <= 0 {
		return "", fmt.Errorf("invalid size")
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
		return "", fmt.Errorf("not found")
	}
	return found, nil
}

func resolveAbsolutePath(relPath, suffix string, size int64) (string, error) {
	if relPath == "" && size <= 0 {
		return "", fmt.Errorf("invalid parameters")
	}
	
	var roots []string
	
	if fallback := getConfigString("music_folder", ""); fallback != "" {
		roots = append(roots, fallback)
	}

	libraries, _ := host.LibraryGetAllLibraries()
	for _, lib := range libraries {
		if lib.MountPoint != "" { roots = append(roots, lib.MountPoint) }
		if lib.Path != "" { roots = append(roots, lib.Path) }
	}

	for _, root := range roots {
		if root == "" {
			continue
		}

		if relPath != "" {
			direct := filepath.Join(root, relPath)
			if realPath, err := filepath.EvalSymlinks(direct); err == nil {
				if stat, err := os.Stat(realPath); err == nil && !stat.IsDir() {
					return realPath, nil
				}
			}
			if stat, err := os.Stat(direct); err == nil && !stat.IsDir() {
				return direct, nil
			}
		}

		if actualPath, searchErr := findAudioBySize(root, suffix, size); searchErr == nil && actualPath != "" {
			if realPath, err := filepath.EvalSymlinks(actualPath); err == nil {
				return realPath, nil
			}
			return actualPath, nil
		}
	}
	return "", fmt.Errorf("not found absolute")
}

func resolveFromRelativePath(relPath string) string {
	if relPath == "" {
		return ""
	}
	
	var roots []string
	if fallback := getConfigString("music_folder", ""); fallback != "" {
		roots = append(roots, fallback)
	}

	libraries, _ := host.LibraryGetAllLibraries()
	for _, lib := range libraries {
		if lib.MountPoint != "" { roots = append(roots, lib.MountPoint) }
		if lib.Path != "" { roots = append(roots, lib.Path) }
	}

	if filepath.IsAbs(relPath) {
		if _, err := os.Stat(relPath); err == nil {
			if realPath, err := filepath.EvalSymlinks(relPath); err == nil {
				return realPath
			}
			return relPath
		}
		
		parts := strings.Split(filepath.ToSlash(relPath), "/")
		if len(parts) >= 3 {
			relPath = filepath.Join(parts[len(parts)-3:]...)
		} else if len(parts) >= 2 {
			relPath = filepath.Join(parts[len(parts)-2:]...)
		} else {
			relPath = filepath.Base(relPath)
		}
	}

	for _, root := range roots {
		if root == "" {
			continue
		}
		fullPath := filepath.Join(root, relPath)

		if realPath, err := filepath.EvalSymlinks(fullPath); err == nil {
			if stat, err := os.Stat(realPath); err == nil && !stat.IsDir() {
				return realPath
			}
		}

		if stat, err := os.Stat(fullPath); err == nil && !stat.IsDir() {
			if absPath, err := filepath.Abs(fullPath); err == nil {
				return absPath
			}
			return fullPath
		}
	}
	
	return ""
}

func getSongDetailsFromSubsonic(username, trackID string) (*subsonicSongResponse, error) {
	if username == "" {
		username = getNavidromeUser()
	}
	jsonStr, err := host.SubsonicAPICall("getSong?id=" + trackID + "&u=" + username + "&f=json&v=1.16.0")
	if err != nil {
		return nil, err
	}
	var resp subsonicSongResponse
	json.Unmarshal([]byte(jsonStr), &resp)
	if resp.SubsonicResponse.Song.Path == "" {
		return nil, fmt.Errorf("relpath failed")
	}
	return &resp, nil
}

func getTrackArtistAndDir(username, trackID, trackArtist, fallbackPath string) (string, string) {
	var abs string
	finalArtist := ""

	if detail, err := getSongDetailsFromSubsonic(username, trackID); err == nil {
		abs, _ = resolveAbsolutePath(detail.SubsonicResponse.Song.Path, detail.SubsonicResponse.Song.Suffix, detail.SubsonicResponse.Song.Size)

		if aArtist := cleanArtistName(detail.SubsonicResponse.Song.AlbumArtist); aArtist != "" {
			finalArtist = aArtist
		} else if art := cleanArtistName(detail.SubsonicResponse.Song.Artist); art != "" {
			finalArtist = art
		}
	}

	if abs == "" {
		abs = resolveFromRelativePath(fallbackPath)
	}
	if finalArtist == "" {
		finalArtist = cleanArtistName(trackArtist)
	}

	if finalArtist == "" && abs != "" {
		parts := strings.Split(filepath.ToSlash(abs), "/")
		if len(parts) >= 3 {
			guessedArtist := parts[len(parts)-3]
			
			lowerGuessed := strings.ToLower(guessedArtist)
			if guessedArtist != "" && 
			   lowerGuessed != "volumes" && 
			   lowerGuessed != "libraries" && 
			   lowerGuessed != "music" && 
			   lowerGuessed != "music library" && 
			   guessedArtist != "." {
				finalArtist = guessedArtist
			}
		}
	}
	return finalArtist, abs
}