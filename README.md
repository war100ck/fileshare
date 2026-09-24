# FileShare

Self-hosted file sharing server: **Web UI + SFTP + FTP** with one shared virtual filesystem.

Share folders from any drive over your LAN or the internet: public download links (with optional password and expiry), one-click ZIP download of folders, and classic SFTP/FTP access for file managers like Total Commander.

[Русская версия ниже](#русская-версия)

## Features

- **Web interface** (`http://<host>:8080`): browse folders, upload/download, create public links (`/s/<token>`) with optional password and expiration, download folders as ZIP.
- **SFTP server** (default port `2222`): same files and accounts; works with WinSCP, FileZilla, Total Commander, `sftp` CLI.
- **FTP server** (default port `21`): passive/active modes, resume (REST), UTF-8 names; works with Total Commander's native FTP dialog.
- **Mounts**: expose any drives/folders as named roots of the virtual filesystem.
- **Users**: bcrypt-hashed passwords, admin flag.
- **CLI downloader**: `fileshare download <url>` with parallel file fetch.
- Optional HTTPS (own certificate), configurable ports/bind, `external_ip` pin for correct public links.

## Requirements

- Go **1.27+** (see `go.mod`)
- Windows, Linux or macOS

## Build

```bash
git clone https://github.com/war100ck/fileshare.git
cd fileshare
go build -o fileshare.exe ./cmd/server     # Windows
# go build -o fileshare ./cmd/server       # Linux/macOS
```

## Run

```bash
./fileshare.exe                # starts server and opens http://127.0.0.1:8080
./fileshare.exe -nobrowser     # without opening browser
./fileshare.exe -config path/to/config.json
```

Default login/password: **admin / admin** — change it immediately in the web UI settings.

On first run the program creates `config.json` and a `data/` folder (`data/` holds the SFTP host key and logs — never publish it).

## Configuration (`config.json`)

| Field | Default | Meaning |
|---|---|---|
| `web_port` | 8080 | Web UI port |
| `sftp_port` | 2222 | SFTP port |
| `ftp_port` | 21 | FTP port |
| `bind` | 0.0.0.0 | Listen address |
| `root_dir` | shared | Legacy root folder (used when no mounts) |
| `mounts` | [] | Named folders `{name, path}` shown as filesystem roots |
| `external_ip` | "" | Pinned public IP for generated links (set it if your machine uses a VPN/WARP, otherwise detection returns the VPN IP) |
| `https`, `tls_cert`, `tls_key` | false | Enable HTTPS with your certificate |
| `zip_max_bytes` | 2 GiB | Max size for ZIP downloads |
| `session_ttl_minutes` | 720 | Web session lifetime |
| `users` | admin | Accounts `{username, password_hash (bcrypt), admin}` |
| `shares` | [] | Public links `{token, name, path, password_hash?, expires?}` |

## Usage

### Web

1. Open `http://<host>:8080`, log in.
2. Browse mounts, upload files.
3. Select a folder → **Create public link** → optionally set password/expiry.
4. Share the link: `http://<host>:8080/s/<token>` — recipient downloads without registration.

To accept connections from the internet, forward ports on your router (e.g. `8080`, `2222`) to this machine and/or pin `external_ip` in settings.

### SFTP

Point any SFTP client at `host:2222` with your web credentials. The root lists your mounts.

### FTP

Connect any FTP client to `host:21` (user/password = web credentials), passive mode recommended.

Total Commander example: *Network* → *FTP connection* → new entry, host `127.0.0.1:21` (or LAN IP), user `admin`, checkbox passive mode.

### CLI downloader

```bash
fileshare download http://192.168.1.5:8080/s/<token> -o ./Downloads -j 8 -password secret
```

## Security notes

- Change the default password immediately.
- Do **not** commit `config.json` (password hashes, share tokens) or `data/` (SFTP **host key**) — they are in `.gitignore` for a reason.
- Prefer HTTPS or trusted networks when exposing the service to the internet.

---

# Русская Версия

Самостоятельно размещаемый сервер обмена файлами: **веб-интерфейс + SFTP + FTP** с одной общей виртуальной файловой системой.

Публикация папок с любых дисков в локальной сети и в интернете: публичные ссылки на скачивание (с паролем и сроком действия), скачивание папок одним ZIP-архивом и классический доступ по SFTP/FTP через файловые менеджеры (Total Commander и т.п.).

## Возможности

- **Веб-интерфейс** (`http://<хост>:8080`): просмотр папок, загрузка/скачивание, публичные ссылки (`/s/<токен>`) с паролем и сроком действия, скачивание папок ZIP-архивом.
- **SFTP-сервер** (порт `2222`): те же файлы и учётки; WinSCP, FileZilla, Total Commander, `sftp`.
- **FTP-сервер** (порт `21`): пассивный/активный режимы, докачка (REST), имена в UTF-8; работает с родным FTP-диалогом Total Commander.
- **Монтирование папок**: любые диски/папки как корни виртуальной ФС.
- **Пользователи**: пароли в виде bcrypt, флаг администратора.
- **Загрузчик в консоли**: `fileshare download <ссылка>` с параллельной загрузкой.
- Опциональный HTTPS, настраиваемые порты/адрес, поле `external_ip` для корректных публичных ссылок (закрепите его, если на машине включён VPN/WARP — иначе в ссылках появится IP VPN).

## Требования

- Go **1.27+** (см. `go.mod`)
- Windows, Linux или macOS

## Сборка

```bash
git clone https://github.com/war100ck/fileshare.git
cd fileshare
go build -o fileshare.exe ./cmd/server     # Windows
# go build -o fileshare ./cmd/server       # Linux/macOS
```

## Запуск

```bash
./fileshare.exe                # сервер + откроется http://127.0.0.1:8080
./fileshare.exe -nobrowser     # без открытия браузера
./fileshare.exe -config путь/к/config.json
```

Логин/пароль по умолчанию: **admin / admin** — сразу смените в настройках веб-интерфейса.

При первом запуске создаются `config.json` и папка `data/` (в `data/` — ключ хоста SFTP и логи; **никогда не публикуйте** её).

## Настройки (`config.json`)

| Поле | По умолчанию | Назначение |
|---|---|---|
| `web_port` | 8080 | Порт веб-интерфейса |
| `sftp_port` | 2222 | Порт SFTP |
| `ftp_port` | 21 | Порт FTP |
| `bind` | 0.0.0.0 | Адрес прослушивания |
| `root_dir` | shared | Корневая папка (когда нет монтирований) |
| `mounts` | [] | Именованные папки `{name, path}` — корни ФС |
| `external_ip` | "" | Фиксированный внешний IP для ссылок (нужен при VPN/WARP) |
| `https`, `tls_cert`, `tls_key` | false | HTTPS по своему сертификату |
| `zip_max_bytes` | 2 ГиБ | Максимальный размер ZIP |
| `session_ttl_minutes` | 720 | Время жизни сессии |
| `users` | admin | Учётки `{username, password_hash (bcrypt), admin}` |
| `shares` | [] | Публичные ссылки `{token, name, path, password_hash?, expires?}` |

## Использование

### Веб

1. Откройте `http://<хост>:8080`, войдите.
2. Перейдите в папку, загружайте файлы.
3. Выберите папку → **Создать публичную ссылку**, при желании задайте пароль/срок.
4. Отправьте ссылку `http://<хост>:8080/s/<токен>` — получатель скачает без регистрации.

Для доступа из интернета пробросьте порты на роутере (например `8080`, `2222`) на эту машину и/или укажите `external_ip` в настройках.

### SFTP

Любой SFTP-клиент → `хост:2222`, логин/пароль как в вебе. В корне — список ваших монтирований.

### FTP

Любой FTP-клиент → `хост:21` (те же учётки), рекомендуется пассивный режим.

Пример для Total Commander: *Сеть* → *FTP-соединение* → новая запись, хост `127.0.0.1:21` (или IP в LAN), пользователь `admin`, включить пассивный режим.

### Загрузчик в консоли

```bash
fileshare download http://192.168.1.5:8080/s/<токен> -o ./Загрузки -j 8 -password секрет
```

## Важно о безопасности

- Сразу смените пароль по умолчанию.
- Для выхода в интернет предпочитайте HTTPS или доверенные сети.
