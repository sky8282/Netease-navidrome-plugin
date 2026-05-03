# Netease-navidrome-plugin
英文曲目多的建议使用 Qobuz 插件：https://github.com/sky8282/Qobuz-navidrome-plugin
## Navidrome 的增强插件，基于网易云音乐 + Qobuz 数据源<br>实现自动补全本地音乐库元数据
✨ 功能特性
* 🖼️ 自动写入专辑封面（ cover.jpg ）
* 👤 自动写入歌手头像（ artist.jpg ）
* 🎼 自动下载歌词（ 曲目名.lrc，翻译合并 ）
* 📖 自动搜索 Qobuz 与下载专辑 PDF 文件（需 token，国内需科学网络）
* 🎼 古典乐 作品 写入曲目元数据供定制版 feishin 读取 （ 请从 Releases 下载定制版 feishin ）

* 📚 自动补全：
    * 专辑简介（ Description ）
    * 歌手简介（ Biography ）
    * 相似歌手（ SimilarArtists，需要 MUSIC_U ）
      * 自行搜索如何获得 MUSIC_U ，格式如下：
      * ( 你的 MUSIC_U );os=pc;appver=8.9.75;
* ⚠️ 需开启硬盘写入权限 rw ( 特别是: 容器 / Nas 版的 navidrome 启动配置里修改 )才能执行以下动作：
    * 歌手头像               cover.jpg
    * 专辑封面               artist.jpg
    * 歌词                  曲目名.lrc
    * 专辑画册               专辑名.pdf（ 需 🇫🇷 法国区 Token ）
    * 增量写入本地音轨元数据   ⚠️ 慎用 ⚠️
    * 专辑元数据             netease_metadata.json
    * 专辑曲目写入记录列表     netease_processed.txt
* ⚡ 内置缓存（ KVStore ），减少 API 请求

## 🧠 插件在以下时机触发：
* ⚠️ 刮削的对象没有被 navidrome 缓存
* ▶️ 播放歌曲（ NowPlaying ）
* 📊 Scrobble 状态上报
* 📀 打开专辑页
* 👤 打开歌手页

## 🚀 从 Releases 下载 netease.ndp 将文件放入 Navidrome 目录下的 plugins 插件文件夹里，并在官方网页里开启插件：
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
AGENTS = "netease,qobuz,deezer,lastfm,listenbrainz"
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

##

<img width="2354" height="2260" alt="1" src="https://github.com/user-attachments/assets/b525e171-e912-43b0-a7fd-c95bcada91d5" />

## 🛠️ 网页里设置与启用插件：
<img width="1616" height="1734" alt="1" src="https://github.com/user-attachments/assets/fbda1a4b-cf53-4644-992f-118f641a7256" />
<img width="1796" height="1570" alt="2" src="https://github.com/user-attachments/assets/a713db0c-d028-4b91-af90-5587fe72edb4" />







