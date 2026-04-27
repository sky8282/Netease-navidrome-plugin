# Netease-navidrome-plugin
Navidrome 的增强插件，基于网易云音乐 + Qobuz 数据源，实现自动补全本地音乐库元数据。

✨ 功能特性
* 🎼 自动下载歌词（.lrc，支持翻译合并）
* 📖 自动下载 Qobuz 专辑 PDF（Booklet）
* 🖼️ 自动写入专辑封面 cover.jpg
* 👤 自动写入歌手头像 artist.jpg
* 📚 自动补全：
    * 专辑简介（Description）
    * 歌手简介（Biography）
    * 相似歌手
* ⚡ 内置缓存（KVStore），减少 API 请求
* ⚠️ 写入歌手头像，专辑封面，歌词 lrc 需开启硬盘写入权限(特别是容器版)

🧠 工作原理，插件在以下时机触发：
* ▶️ 播放歌曲（NowPlaying）
* 📊 Scrobble 上报
* 📄 请求歌词
* 📀 打开专辑页
* 👤 打开歌手页

🚀 将文件放入 Navidrome 插件目录，并在官方网页里开启插件：
```text
/plugins/
└── mnetease.ndp
```
🛠️ 启用插件示列：
```text
AGENTS = "netease,deezer,lastfm,listenbrainz"
PLUGINS_ENABLED = true
PLUGINS_FOLDER = "./plugins"
PLUGINS_AUTORELOAD = true
PLUGINS_LOGLEVEL = "INFO"
PLUGINS_CACHESIZE = "200MB"
```
