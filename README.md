# Netease-navidrome-plugin
## Navidrome 的增强插件，基于网易云音乐 + Qobuz 数据源，实现自动补全本地音乐库元数据
✨ 功能特性
* 🖼️ 自动写入专辑封面 cover.jpg
* 👤 自动写入歌手头像 artist.jpg
* 🎼 自动下载歌词（曲目名.lrc，翻译合并）
* 📖 自动搜索 Qobuz 与下载专辑 PDF 文件（需 token，国内需科学网络）

* 📚 自动补全：
    * 专辑简介（Description）
    * 歌手简介（Biography）
    * 相似歌手
* ⚠️ 需开启硬盘写入权限 rw (特别是: 容器 / Nas 版)才能执行以下动作：
    * 歌手头像               cover.jpg
    * 专辑封面               artist.jpg
    * 歌词                  曲目名.lrc
    * 专辑画册               专辑名.pdf
    * 专辑元数据             netease_metadata.json
    * 曲目写入元数据
    * 专辑曲目写入记录列表     netease_processed.txt
* ⚡ 内置缓存（KVStore），减少 API 请求

## 🧠 插件在以下时机触发：
* ▶️ 播放歌曲（NowPlaying
* 📊 Scrobble 上报
* 📀 打开专辑页
* 👤 打开歌手页

## 🚀 从 Releases 下载 netease.ndp 将文件放入 Navidrome 根目录下的 plugins 插件文件夹里，并在官方网页里开启插件：
```text
/plugins/
└── netease.ndp
```
## 🛠️ 或者自行编译：
1. 安装依赖
```text
go mod init netease-plugin&&go mod tidy
```
2. 编译 wasm 如报警自行安装所需的工具:
```text
tinygo build -opt=2 -scheduler=none -no-debug -o plugin.wasm -target wasip1 -buildmode=c-shared .
```
3. 打包成 ndp:
```text
zip netease.ndp plugin.wasm manifest.json
```
## 🛠️ 启用插件示列：
```text
AGENTS = "netease,deezer,lastfm,listenbrainz"
PLUGINS_ENABLED = true
PLUGINS_FOLDER = "./plugins"
PLUGINS_AUTORELOAD = true
PLUGINS_LOGLEVEL = "INFO"
PLUGINS_CACHESIZE = "200MB"
```
## 📖 歌手头像 / 专辑封面 / 歌词 / PDF 保存路径格式:
```text
/歌手名文件夹/
└── artist.jpg （歌手头像）
└── 专辑名文件夹
    └── cover.jpg （专辑封面）
    └── 曲目名.lrc （歌词文件）
    └── 专辑名.pdf （Qobuz_PDF）
    └── netease_metadata.json （专辑元数据文件）
    └── netease_processed.txt （写入元数据的曲目列表文件）
    └── 曲目1
    └── 曲目2
```
<img width="930" height="842" alt="1" src="https://github.com/user-attachments/assets/f8a730c5-736c-4d49-8198-2eef5bb5271c" />

## 🛠️ 网页里设置与启用插件：

<img width="1628" height="1808" alt="1" src="https://github.com/user-attachments/assets/1d4adfe6-c0a6-4269-9706-04d87abde645" />
<img width="1610" height="1276" alt="2" src="https://github.com/user-attachments/assets/19a3329c-f456-4a93-9994-2c6858608e15" />






