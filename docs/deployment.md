# 部署与运维

本文介绍怎么安装、作为系统服务运行、管理数据和备份，以及监控和隐私方面的注意事项。进程拓扑和重启行为见 [架构](architecture.md#1-部署拓扑)。

## 安装

**预编译二进制（推荐）**：在 [Releases](https://github.com/kinako404/conloom/releases) 下载对应平台的压缩包 `conloom_<版本>_<系统>_<架构>.tar.gz`（Windows 为 `.zip`），解压即可，不需要任何运行时。可以用同页的 `checksums.txt` 校验：

```sh
sha256sum -c checksums.txt --ignore-missing
```

压缩包内含 `conloom` 二进制、`README.md`、`LICENSE`、`conloom.example.yaml`、`deploy/` 和 `docs/`。macOS 首次运行如果被拦截，可以执行 `xattr -d com.apple.quarantine ./conloom`。

**从源码安装**：需要 Go 1.26 或更高版本（由纯 Go 的 SQLite 驱动决定）。

```sh
go install github.com/kinako404/conloom/cmd/conloom@latest
```

## 进程与网络

- 生产环境运行核心 `conloom serve`，以及按需接入的适配器：`conloom adapter napcat`（QQ）、`conloom adapter telegram`（Telegram），都是长期进程。适配器不监听端口，只主动连接平台（NapCat 或 Telegram Bot API）和核心，可以部署在另一台机器上。
- 核心只能运行一个，每个机器人实例的适配器也只能运行一个：核心的调度、限流等状态在进程内存里，适配器的去重记录是进程本地的。
- API、`/healthz`、`/metrics` 和 Web 管理面板（配置了 `panel` 段时）共用 `server.listen` 一个端口，默认只监听本机；设了 `panel.listen` 时面板改到那个单独的端口（见 [用单独端口直接访问](#用单独端口直接访问)）。`/healthz` 与 `/metrics` 不需要鉴权；如果要让别的机器上的适配器访问核心而把 `server.listen` 改为非本地地址，请用防火墙限制来源，或关闭 `metrics_enabled`。适配器和核心之间是普通 HTTP，跨机器时建议走内网或加一层 TLS 代理。
- 仓库不负责部署 NapCat，只要求 NapCat 开启 OneBot 11 正向 WebSocket 服务并设置 access token。适配器断线后自动重连（退避从 1 秒起，最长 30 秒）。

## Linux（systemd）

仓库的 [`deploy/systemd/`](../deploy/systemd) 提供三个单元：`conloom.service`（核心），以及 `conloom-napcat.service` 和 `conloom-telegram.service`（适配器，依赖核心，按需安装）。它们都以 `conloom` 用户运行、异常退出 3 秒后自动重启，停止时最多等待 90 秒（适配器会等正在发送的消息拿到结果再退出）。退出码为 78 时不重启（`RestartPreventExitStatus=78`）：78 是配置错误（sysexits 的 `EX_CONFIG`），比如配置文件读不了、校验失败、Telegram 的 token 或核心令牌被拒绝、已经迁移到数据库的 `persona_templates` 或 `bots[]` 的设置键还留在配置里，重启也没用，`systemctl status` 会显示 `status=78`，改好配置后手动 `systemctl restart`。其他异常退出照常重启，包括 Go 程序 panic（退出码 2，日志里有堆栈）。单元启用了 `ProtectSystem=strict`，只有 `/opt/conloom/data` 可写：该目录必须在启动前存在；如果把 `storage.data_dir` 或适配器的 `state_dir` 改到别处，要同步修改单元里的 `ReadWritePaths`。

```sh
sudo useradd --system --home-dir /opt/conloom --shell /usr/sbin/nologin conloom
sudo mkdir -p /opt/conloom/data
sudo cp conloom conloom.yaml .env /opt/conloom/
sudo chown -R conloom:conloom /opt/conloom
sudo chmod 600 /opt/conloom/.env
sudo chmod 700 /opt/conloom/data
sudo cp deploy/systemd/conloom.service deploy/systemd/conloom-napcat.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now conloom conloom-napcat
journalctl -u conloom -u conloom-napcat -f
```

`chmod 700` 那一步让数据目录只有服务用户能访问，原因见 [数据目录与权限](#数据目录与权限)。

**Telegram 适配器**用 `conloom-telegram.service`。它的去重记录 `telegram-dedup.log` 写在 `adapters.telegram.state_dir`（默认同 `storage.data_dir`），改到别处时同样要改 `ReadWritePaths`。只接 Telegram 时可以不装 NapCat 的单元：

```sh
sudo cp deploy/systemd/conloom-telegram.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now conloom-telegram
journalctl -u conloom -u conloom-telegram -f
```

适配器需要能访问 Telegram 的 Bot API。要走代理时，用 `sudo systemctl edit conloom-telegram` 加上 `[Service]` 段的 `Environment=HTTPS_PROXY=http://<代理地址>:<端口>`；`.env` 只用于展开配置变量，不会设置进程的环境变量。更换 token 的步骤见 [接入 Telegram](telegram.md#5-重新生成-token)。

## macOS（launchd）

保存为 `~/Library/LaunchAgents/io.github.kinako404.conloom.plist`（路径改成你的实际目录）：

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>io.github.kinako404.conloom</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/<你的用户名>/conloom/conloom</string>
    <string>serve</string>
    <string>-c</string>
    <string>/Users/<你的用户名>/conloom/conloom.yaml</string>
  </array>
  <key>WorkingDirectory</key><string>/Users/<你的用户名>/conloom</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>/Users/<你的用户名>/conloom/conloom.log</string>
</dict>
</plist>
```

```sh
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/io.github.kinako404.conloom.plist
```

适配器同理：复制一份，`Label` 改为 `io.github.kinako404.conloom-napcat`，参数改为 `adapter napcat -c …`，日志文件另起名字。

## Windows（NSSM）

`conloom.exe` 是普通控制台程序，不实现 Windows 服务控制协议，不能直接用 `sc.exe create` 注册（启动会报 1053）。推荐用 [NSSM](https://nssm.cc/) 包装：

```bat
nssm install ConLoom C:\conloom\conloom.exe serve -c C:\conloom\conloom.yaml
nssm set ConLoom AppDirectory C:\conloom
nssm set ConLoom AppStderr C:\conloom\conloom.log
nssm start ConLoom

nssm install ConLoomNapCat C:\conloom\conloom.exe adapter napcat -c C:\conloom\conloom.yaml
nssm set ConLoomNapCat AppDirectory C:\conloom
nssm set ConLoomNapCat AppStderr C:\conloom\conloom-napcat.log
nssm set ConLoomNapCat DependOnService ConLoom
nssm start ConLoomNapCat
```

## 向量服务

语义检索是可选的（[配置参考](configuration.md#embedding)，工作方式见 [架构 5.7 节](architecture.md#57-长期回忆的检索)）。核心只调用 OpenAI 兼容的 `POST <endpoint>/embeddings`，任何兼容的服务都可以用。下面是一种做法：在另一台机器（下文的 `<向量主机>`）上用 [Ollama](https://ollama.com/) 纯 CPU 运行 bge-m3，核心通过一条专用的 SSH 隧道访问它，向量服务本身不对外开放端口。

实测参考（Ollama 0.40.16、bge-m3、纯 CPU）：单句约 50ms，向量 1024 维；经隧道访问 `http://127.0.0.1:11434/v1` 时单次往返约 440ms，其中网络约 184ms。批量请求时每条仍约 50ms，但省掉了多次网络往返，所以 `batch_size` 默认 16。检索时把查询句转成向量另有 2 秒超时，向量服务慢或不可用时语义检索返回空结果，不影响回复；连续失败 3 次后 60 秒内不再请求它，之后先放一个请求试探。

**1. 在向量主机上运行 Ollama（纯 CPU）**

按 Ollama 的官方说明安装，然后让它只用 CPU、只监听本机，二选一：

- 删掉安装目录里的 GPU 运行库（例如 `lib/ollama/` 下的 `cuda_*`、`rocm` 等目录；升级 Ollama 后要重新删）；
- 或者在服务环境里设 `CUDA_VISIBLE_DEVICES=-1`（AMD 显卡另设 `ROCR_VISIBLE_DEVICES=-1`）：

```sh
sudo systemctl edit ollama
```

```ini
[Service]
Environment=CUDA_VISIBLE_DEVICES=-1
Environment=OLLAMA_HOST=127.0.0.1:11434
Environment=OLLAMA_KEEP_ALIVE=-1
```

`OLLAMA_KEEP_ALIVE=-1` 让模型一直留在内存里：Ollama 默认空闲 5 分钟就卸载模型，下一次查询要先重新加载，会超过检索的 2 秒超时，这次语义检索就落空了。

```sh
sudo systemctl restart ollama
ollama pull bge-m3
curl -s http://127.0.0.1:11434/v1/embeddings -d '{"model":"bge-m3","input":["你好"]}' | head -c 200
```

`ollama ps` 的 PROCESSOR 一列应当是 `100% CPU`。

**2. 准备隧道专用的用户和密钥**

在核心所在的机器上建一个只用来开隧道的系统用户，生成没有口令的专用密钥：

```sh
sudo useradd --system --create-home --home-dir /var/lib/conloom-tunnel --shell /usr/sbin/nologin conloom-tunnel
sudo -u conloom-tunnel mkdir -m 700 /var/lib/conloom-tunnel/.ssh
sudo -u conloom-tunnel ssh-keygen -t ed25519 -N '' -C conloom-embed-tunnel -f /var/lib/conloom-tunnel/.ssh/id_ed25519
```

在向量主机上同样建一个用户（下文为 `embed-tunnel`），把公钥写进它的 `~/.ssh/authorized_keys`，并加上限制：

```text
restrict,port-forwarding,permitopen="127.0.0.1:11434",command="/bin/false" ssh-ed25519 AAAA…（公钥） conloom-embed-tunnel
```

`restrict` 关掉终端、代理转发、X11 和端口转发等一切功能，`port-forwarding` 只把本地端口转发加回来，`permitopen` 限定只能转发到向量主机本机的 11434 端口，`command="/bin/false"` 让这把钥匙登录后什么也执行不了。向量主机的 sshd 要允许这个用户做 TCP 转发（`AllowTcpForwarding` 默认为 `yes`）。

然后在核心机器上记下向量主机的主机密钥（请核对指纹），并试连一次：

```sh
sudo -u conloom-tunnel sh -c 'ssh-keyscan -t ed25519 <向量主机> >> /var/lib/conloom-tunnel/.ssh/known_hosts'
sudo -u conloom-tunnel ssh -N -L 127.0.0.1:11434:127.0.0.1:11434 embed-tunnel@<向量主机>
```

**3. 用 systemd 保持隧道**

保存为 `/etc/systemd/system/conloom-embed-tunnel.service`：

```ini
[Unit]
Description=SSH tunnel from ConLoom to the embedding service
Wants=network-online.target
After=network-online.target
Before=conloom.service

[Service]
User=conloom-tunnel
ExecStart=/usr/bin/ssh -N -T \
  -o BatchMode=yes -o ExitOnForwardFailure=yes \
  -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
  -o StrictHostKeyChecking=yes \
  -i /var/lib/conloom-tunnel/.ssh/id_ed25519 -o IdentitiesOnly=yes \
  -L 127.0.0.1:11434:127.0.0.1:11434 \
  embed-tunnel@<向量主机>
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now conloom-embed-tunnel
```

隧道只监听核心机器的 `127.0.0.1:11434`。如果核心机器上本来就有服务占用这个端口，换一个本地端口（例如 `-L 127.0.0.1:11435:127.0.0.1:11434`），配置里的 `endpoint` 跟着改。

**4. 打开语义检索**

在 `conloom.yaml` 里加上：

```yaml
embedding:
  endpoint: http://127.0.0.1:11434/v1
  model: bge-m3
```

重启核心。之后每分钟一轮，后台为记忆已生效的会话生成向量。初次开启时会从每个会话的第一条消息开始回溯全部历史（和关键词索引的补建一样），每轮最多 200 段、每小时约 1.2 万段，所以历史较长的会话要一段时间才能补齐：一个很活跃的大群一年约 9 万段，补齐要 7 个多小时。

首次回填的头几分钟里，向量服务忙着批量生成，查询句转向量可能超过 2 秒的超时；连续 3 次超时会触发熔断，一分钟内跳过语义检索、只走关键词，日志里出现 `embedding the query failed; no semantic hits`。这是正常现象，回填的压力降下来以后会自动恢复。

资源量级：每段的向量约 4KB（1024 维），这样的大群一年约 9 万段，`conloom.db` 里多出约 370MB，核心检索它时内存里也要约 370MB；最近检索过的最多 8 个会话会同时留在内存里（一个 bot 的面板聊天多于 8 个时，检索面板聊天后留住它的全部面板聊天），按会话规模之和估算内存。群聊里有人 `/忘记我` 时，删段在同一个写事务里按段扫描，9 万段的群约 1 秒，期间其他写入稍等。可以看 `/metrics` 的 `conloom_embedding_requests_total{result="ok"}` 是否在增长、`conloom_embedding_backlog`（待生成段数的估计值）是否在下降。隧道断开或 Ollama 停止时，日志里出现 `embedding round failed`，后台按 1、2、4、8 分钟直到最长 10 分钟的间隔重试，恢复后自动继续；语义检索在这期间返回空结果。

换模型时改 `model` 后重启即可：后台按会话从新到旧重新生成向量，补齐之前旧模型的向量不参与检索，全部补齐后删除。换向量服务（换机器、换 Ollama 以外的实现，哪怕模型同名）时也要改 `model`：先在新服务上给模型起个新名字（如 `ollama cp bge-m3 bge-m3-b`），再把 `model` 改成它，否则后台只能靠维数不同来发现，维数相同的不同向量会被混在一起比较。

## Web 管理面板

面板是可选的，功能见 [Web 管理面板](panel.md)。下面按 systemd 布局（`/opt/conloom`）说明。

**1. 生成密码和哈希**

`conloom panel hash-password` 从标准输入读一行作为密码（去掉行尾的换行），输出一行 `pbkdf2-sha256:<迭代次数>:<盐>:<哈希>`，默认 600000 次迭代。生成一个随机密码，存在只有 root 能读的文件里，再算出哈希：

```sh
sudo sh -c 'umask 077 && openssl rand -base64 18 > /root/conloom-panel-password'
sudo sh -c '/opt/conloom/conloom panel hash-password < /root/conloom-panel-password'
```

登录时要用这个密码，需要时用 root 读出来（文件只有 root 能读）：

```sh
sudo cat /root/conloom-panel-password
```

也可以直接运行 `conloom panel hash-password` 后键入密码回车，但密码会显示在终端上。密码不要写进仓库、命令行参数或日志。

**2. 写入 `.env` 和配置**

把输出的整行写进 `/opt/conloom/.env`，形如：

```sh
CONLOOM_PANEL_ADMIN_HASH=pbkdf2-sha256:600000:<盐>:<哈希>
```

不想手动复制的话，下面这一行重新算一次哈希，算成功了才追加到 `.env` 末尾（每次算出的盐不同，哈希和第 1 步输出的不一样，但同样对应这个密码）：

```sh
sudo sh -c 'h=$(/opt/conloom/conloom panel hash-password < /root/conloom-panel-password) && printf "\nCONLOOM_PANEL_ADMIN_HASH=%s\n" "$h" >> /opt/conloom/.env'
```

哈希里没有 `$`，放进 `.env` 或 shell 都不会被当成变量展开。`.env` 里同一个变量出现多次时以最后一行为准。然后在 `conloom.yaml` 里加上 `panel` 段（字段见 [配置参考：panel](configuration.md#panel)），重启核心：

```yaml
panel:
  users:
    - name: admin
      password_hash: ${CONLOOM_PANEL_ADMIN_HASH}
```

```sh
sudo systemctl restart conloom
```

**3. 经 SSH 隧道访问**

核心默认只监听 `127.0.0.1:8787`，在自己的电脑上开一条隧道：

```sh
ssh -L 8787:127.0.0.1:8787 <用户>@<服务器>
```

然后在浏览器打开 `http://127.0.0.1:8787/panel/`，用 `admin` 和第 1 步的密码登录。这种方式 `cookie_secure` 保持 `false`。

**改密码**：重新执行第 1、2 步，替换 `.env` 里的哈希并重启核心（用上面的一行追加时，先用 `sudo sed -i '/^CONLOOM_PANEL_ADMIN_HASH=/d' /opt/conloom/.env` 删掉旧的那行）。这个用户已有的登录会话随即全部失效（会话记着登录时哈希的指纹，和配置里的不一致就拒绝），其他用户不受影响。忘记密码时也是这样重置。

### 经 HTTPS 反向代理访问

需要不经 SSH 直接访问时，在核心前面放一个负责 HTTPS 的反向代理，只转发 `/panel` 下的路径，核心仍然只监听本机。以 [Caddy](https://caddyserver.com/) 为例（`panel.example.invalid` 换成你的域名，Caddy 会自动申请证书）：

```text
panel.example.invalid {
	handle /panel* {
		reverse_proxy 127.0.0.1:8787
	}
	handle {
		respond 404
	}
}
```

用 nginx 时（证书配置从略）：

```nginx
server {
    listen 443 ssl;
    server_name panel.example.invalid;

    location /panel {
        proxy_pass http://127.0.0.1:8787;
        proxy_set_header Host $http_host;
        client_max_body_size 64m;
    }
    location / {
        return 404;
    }
}
```

`Host` 必须用 `$http_host`（浏览器发来的原样 `Host`，带端口）：面板的 CSRF 检查比较请求的 `Origin` 和 `Host`，nginx 的 `$host` 会去掉端口，代理不在 443 端口上时两者就对不上，登录和所有修改都会返回 403。

同时在 `conloom.yaml` 里设 `cookie_secure: true` 并重启核心：

```yaml
panel:
  cookie_secure: true
  users:
    - name: admin
      password_hash: ${CONLOOM_PANEL_ADMIN_HASH}
```

注意：

- 代理必须原样转发 `Host` 头：面板的 CSRF 检查要求请求的 `Origin` 和 `Host` 一致。Caddy 默认就这样做；nginx 要写 `proxy_set_header Host $http_host;`（见上面的例子）。
- 面板最大的请求体是导入的人设模板文件，最多 64 MiB；上传人设参考图最多 8 MiB。导出文件里内嵌原图，一个带大参考图的模板导出后就可能超过 9 MiB。nginx 默认只收 1 MiB，要像上面那样把 `client_max_body_size` 调到 `64m`，否则大一点的图或导入文件在代理那里就被拒绝（413），面板只显示“请求失败（HTTP 413）”。
- 不要把 `/api/v1`、`/metrics`、`/healthz` 一起暴露到公网，上面的配置只转发 `/panel`。
- 面板只信任 TCP 连接的对端地址，不读 `X-Forwarded-For`，经代理时所有人的来源 IP 都是代理的地址：登录失败的“每 IP 15 分钟 5 次”额度由所有访问者共用，有人在公网上乱试密码时，你自己也可能要等最多 15 分钟才能再登录。
- 设了 `cookie_secure: true` 以后浏览器只经 HTTPS 发送登录 cookie，再用 SSH 隧道的 `http://` 地址访问可能登录不上，这时改用 HTTPS 地址。
- 也可以让面板单独监听本机的一个端口（`panel.listen: 127.0.0.1:8788`），代理转发到 `127.0.0.1:8788`。这个端口上只有面板，代理即使整站转发也不会带出 API 和 `/metrics`。

### 用单独端口直接访问

不想装反向代理时，可以让核心多开一个只提供面板的端口，直接对外监听；`server.listen` 保持 `127.0.0.1:8787`，API、`/healthz`、`/metrics` 不会因此暴露：

```yaml
panel:
  listen: 0.0.0.0:8788
  users:
    - name: admin
      password_hash: ${CONLOOM_PANEL_ADMIN_HASH}
```

```sh
sudo systemctl restart conloom
sudo journalctl -u conloom | grep 'panel listening'
```

然后在浏览器打开 `http://<服务器地址>:8788/panel/`。这个端口上 `/` 跳转到 `/panel/`，`/api/v1/*`、`/healthz`、`/metrics` 返回 404；`server.listen` 上不再有 `/panel/`，用 SSH 隧道时改为转发 8788 端口。

这个端口上一个请求连同请求体要在 30 秒内传完。上行带宽不到约 2 Mbit/s 时（比如手机信号差），8 MiB 的参考图传不完，大的导入文件更是如此，连接被断开，面板显示“无法连接服务器”；这时换个网络，或者用图小一些的文件。

**警告：这样访问是普通 HTTP，登录密码和会话 cookie 都以明文经过网络**，路上的任何人都可能截获并冒用（拿到 cookie 就是管理员）。按推荐程度：

1. 最好仍在前面放 HTTPS 反向代理（见上一节的 Caddy 示例，`panel.listen` 改为 `127.0.0.1:8788`、代理转发到这个端口），并设 `cookie_secure: true`。
2. 至少用防火墙只放行自己的 IP，例如 ufw（`203.0.113.7` 换成你的地址）：

   ```sh
   sudo ufw allow from 203.0.113.7 to any port 8788 proto tcp
   sudo ufw deny 8788/tcp
   ```

   云服务器还要在安全组里同样只放行自己的 IP。

另外，登录限流有一个全局上限：全部来源合计 15 分钟内登录失败 30 次后，所有新的登录都返回 429，直到最早的失败移出 15 分钟窗口（详见 [Web 管理面板：登录与安全](panel.md#登录与安全)）。端口对整个互联网开放时，任何人都能乱试密码把这个上限用完，让你在最多 15 分钟内无法登录；已经登录的会话不受影响，可以继续使用。这也是建议用防火墙限制来源的原因之一。

### 人设参考图和识图模型

人设模板和会话人设可以带一张外观参考图，在面板里上传，用法见 [Web 管理面板：人设模板和外观参考图](panel.md#人设模板和外观参考图)。

**识图模型（可选）**：面板的“生成外观介绍”要用 `model_profiles.vision`，没有配置时这个功能返回“未配置识图模型”，其他功能不受影响。推荐的写法（地址和密钥可以沿用长上下文模型的环境变量，模型名换成你的服务里支持图片输入的模型）：

```yaml
model_profiles:
  vision:
    endpoint: ${CONLOOM_LONG_MODEL_ENDPOINT}
    model: <支持图片输入的模型名>
    api_key: ${CONLOOM_LONG_MODEL_KEY}
    modalities: [text, image]       # 必须包含 image
    context:
      native_window: 131072
      configured_window: 131072
      soft_input_budget: 16000
      reserved_output_tokens: 4096  # 作为 max_tokens；推理模型的思考也算在内，要留足
      safety_margin_tokens: 1024
    runtime:
      max_concurrency: 1
      timeout: 40s
      max_retries: 1
    privacy:
      data_location: remote
      allow_sensitive_content: true
```

`vision` 只用来识图，回复永远不会路由到它。发给它的是长边不超过 1536 像素的 JPEG，最多约 3000 个 token；一次识图最多 40 秒。字段说明见 [配置参考：model_profiles](configuration.md#model_profiles)。

**回复时带图**：只有回复路由到的模型在 `modalities` 里声明了 `image`，回复请求才附带参考图的缩小图。确认回复模型确实接受图片（先在面板里用一个带图的人设试一次回复）之后，再把 `short_context`（以及需要的话 `long_context`）改成 `modalities: [text, image]`；模型不接受图片却声明了 `image` 时，带图的回复会失败。

**资源**：

- 图片存在 `conloom.db` 里：每张是原图（最多 8 MiB）加一份几十 KB 的缩小图。被任何人设模板或人设版本（包括历史版本）引用的图片一直保留，所以数据库会随着上传的图片变大，备份文件也一样（`conloom backup` 导出的快照包含图片）。没有被引用的图片上传满 7 天后由清理任务删除。
- 解码一张接近上限（2500 万像素）的图要几百 MB 内存。面板同时只处理一个上传和一个识图请求，另一个请求同时到达时返回 409，稍后重试即可；服务器内存紧张时先把图缩小再上传。
- 用 `panel.listen` 直接访问时，整个请求（包括图片）必须在 30 秒内传完；网络很慢时先把图缩小。

## 数据目录与权限

全部状态在一个数据目录里，全部配置在 `conloom.yaml` 加可选的 `.env`，二进制没有其他依赖：

```text
<data_dir>/
  conloom.db         SQLite 数据库（运行时旁边还有 conloom.db-wal、conloom.db-shm）
  napcat-dedup.log   NapCat 适配器的已发送记录（按 action_id 去重，保留 24 小时）
```

- `napcat-dedup.log` 写在 `adapters.napcat.state_dir` 下，默认与 `storage.data_dir` 相同；适配器部署在另一台机器时，它在适配器那台机器上。
- 核心第一次创建数据目录时权限为 0700，但目录已经存在时不会修改它。数据库文件按进程的 umask 创建（常见的 umask 022 下是 0644），所以请自己把数据目录设为 0700，或在服务单元里设置 `UMask=0077`。去重记录文件为 0600。
- 核心每小时（以及启动时）清理一次流水数据，分批删除，不长时间占用写锁：

  | 数据 | 表 | 保留 |
  |---|---|---|
  | 已结束（成功、失败、取消）的任务，连同它们的发件箱动作 | `tasks`、`outbox_actions` | `runtime.ops_retention`（默认 30 天），且不短于 `idempotency_retention`；还有待投递或已租出动作的任务不删 |
  | 已确认、已过期或已取消（私聊里被打断而作废的分条）、所属任务已不存在的发件箱动作 | `outbox_actions` | 同上 |
  | 审计记录 | `audit_events` | `runtime.audit_retention`（默认 90 天） |
  | 事件幂等记录（含被处理事件的原文） | `events` | `runtime.idempotency_retention`（默认 7 天） |
  | 主动搭话记录 | `proactive_log` | 7 天 |
  | 私聊状态条的变化记录 | `conversation_buff_events` | 30 天 |
  | 机器人在私聊里给自己加的效果 | `conversation_effects` | 到时间后删除 |
  | 人设外观参考图（原图和缩小图） | `persona_images` | 没有被任何人设模板或人设版本（包括历史版本）引用、上传满 7 天后删除 |
  | 记忆修订历史 | `memory_revisions` | 30 天（由记忆层清理，不受 `ops_retention` 影响） |
  | 模型用量明细（每次模型调用一行） | `model_calls` | `runtime.usage_retention`（默认 400 天） |
  | Web 管理面板的登录会话 | `panel_sessions` | 超过 `panel.session_ttl` 或 `panel.idle_timeout` 后删除 |

- **永远保留**：聊天消息 `messages`（长期记忆的来源）、会话和用户、记忆（`group_memory`、`member_memory`）、群策略版本、人格版本、群友的主动档位和兴趣画像（`conversation_members`）。它们会随使用增长，删除只能靠 `/忘记我` 等功能或手工处理。排队中和运行中的任务也不会被清理，重启后照常恢复。
- 清理以后，`GET /api/v1/tasks/{task_id}` 查询已删除的任务返回 404；聊天消息、引用回复的解析和记忆整理都不依赖这些表。清理只删行，不收缩数据库文件；删掉的空间会被之后的写入复用。

## 备份、迁移与升级

**备份**：服务运行中也可以导出一致快照，目标文件必须不存在；备份文件一创建就是 0600 权限，备份失败时会删掉它：

```sh
./conloom backup -c conloom.yaml -o conloom-backup.db
```

备份是数据库当时的完整快照，已经被清理的任务、发件箱动作和审计记录不在其中；需要更久的任务或审计历史时，调大 `runtime.ops_retention` / `runtime.audit_retention`，或者定期保留备份文件。从备份恢复后，核心照常按当前配置清理过期的流水数据。

`conloom backup` 会以当前二进制的版本打开数据库，必要时先执行表结构迁移。所以不要用更新版本的二进制去备份一个仍由旧版本运行的库。数据库的恢复方法始终是把备份文件放到 `<data_dir>/conloom.db`（确认该目录下没有遗留的 `conloom.db-wal`、`conloom.db-shm`）。

**迁移到另一台机器**：

1. 在旧机器上导出快照（见上文）。
2. 在新机器下载对应平台的二进制，拷贝 `conloom.yaml`、`.env`、备份文件，以及 `napcat-dedup.log`（如果适配器也一起迁移）。
3. 把备份文件放到新机器的 `<data_dir>/conloom.db`，`napcat-dedup.log` 放到新机器的 `adapters.napcat.state_dir`（默认即 `<data_dir>`）。按上文 systemd 布局部署时，恢复的文件要交给服务用户：

   ```sh
   sudo chown conloom:conloom /opt/conloom/data/conloom.db /opt/conloom/data/napcat-dedup.log
   sudo chmod 600 /opt/conloom/data/conloom.db /opt/conloom/data/napcat-dedup.log
   ```

4. 停止旧实例（先停适配器再停核心），启动新实例（先启核心再启适配器）。

备份之后到停止旧实例之前收到的消息不会出现在备份里；如果要一条不丢，可以先停止旧实例再执行 `conloom backup`（服务停止时同样可用）。核心和适配器可以分别迁移：适配器只需要能访问 NapCat 和核心的 API 地址。

**升级**：先备份，再替换二进制并重启（先核心后适配器）。核心启动时自动按顺序应用新的表结构迁移，迁移只能前进、没有回滚脚本，所以回退到旧版本时要用升级前的备份。升级前请对照 [配置参考](configuration.md#群策略) 检查已移除的配置字段（例如 `proactive.contact_cooldown`），配置文件里残留未知字段会导致启动失败。流水表清理没有新增表结构迁移，旧二进制可以直接在同一个库上运行；但回退到没有流水表清理的版本时，要先从 `conloom.yaml` 里删掉 `runtime.ops_retention` 和 `runtime.audit_retention`。

**升级到记忆字数上限可配置的版本**（`memory.summary_max_chars`、`note_max_chars`）：摘要和笔记的默认上限变长（群聊 1500 / 300 字、私聊 3000 / 1000 字），记忆整理的输出也跟着变长。升级时把 `long_context` 的 `reserved_output_tokens` 调到 8192 或以上：低于 8192（且大于 0）而默认策略开启了记忆时，启动日志会有一条 `long_context reserved_output_tokens is low for memory consolidation` 警告；整理时一个人的摘要加笔记放不下，就按比例缩短这次提示词要求的字数（不低于 200 / 50 字；写入时仍按策略的上限截断，已有的较长笔记不会被截短），并为这个会话记一条 `memory consolidation limits reduced …` 警告。调大输出预留并重启后恢复完整上限。已有的摘要和笔记不会立即变长，要经过之后的几次整理才写长。回复时带上的记忆块不超过短模型输入预算（`soft_input_budget`，会话设了 `context.max_context_tokens` 时取两者算出的较小值）的 30%，摘要太长时只带最新的一段，所以写长的记忆不会挤掉近期历史（见 [架构 5.5 节](architecture.md#55-记忆和好感度)）。

**升级到有面板 bot 和在线人设模板的版本**（迁移 `0013_panel_bots.sql`、`0014_persona_templates.sql`）：两个迁移只新增表——0013 是面板 bot 的 `panel_bots`，0014 是在线写入的人设模板 `persona_templates` 和外观参考图 `persona_images`——不改已有的表，启动时自动执行。配置文件里 bot 的 `id` 不能再以 `panel-` 开头（这个前缀留给面板 bot），有的话先改名再升级。

**升级到人设模板只存在数据库里的版本**（迁移 `0015_app_meta.sql`）：配置文件不再支持 `persona_templates`，模板只在 Web 管理面板里管理（见 [配置参考：人设模板](configuration.md#人设模板)）。迁移只新增键值表 `app_meta`，启动时自动执行。

1. 先备份（`conloom backup`），配置文件原样保留，替换二进制后启动核心。
2. 第一次启动时，配置里的 `persona_templates` 自动导入数据库：库里还没有的名字写入，已经有同名模板的（在面板里改过或新建的）跳过，以库里为准，在线改过的版本不会被配置里的旧版本覆盖。跳过的模板在配置里的版本不进数据库，第 3 步删掉配置里的这一段后就没有了；只被这些配置版本引用的外观参考图，上传满 7 天的在这次启动时就跑的维护里删除（之后每小时一次，不满 7 天的到满 7 天后删除）。想留下某个配置版本，先从配置文件或第 1 步的备份里把它抄出来，在面板里另建一个模板。整个导入在一个事务里，最后在 `app_meta` 记下 `config_templates_migrated`（迁移时间）。日志里每个导入或跳过的模板各有一条，审计里有一条 `persona.template.migrate`，列出导入和跳过的模板名，最后一条警告提示删除配置里的 `persona_templates`。
3. 确认面板模板页里模板都在，然后从 `conloom.yaml` 删掉整段 `persona_templates`（先备份这个文件），重启。之后配置里还留着这一段时启动失败，退出码 78（systemd 不会反复重启），报错说明模板已经迁到数据库，请删除配置里的 `persona_templates`。
4. 模板引用的外观参考图 ID 原样写入；图片不存在时回复照常、不带图，并记一条警告，在面板里重新上传同一张图即可（同一张图得到同一个 ID）。

配置 bot 的默认模板（当时还是 `persona_template`，现在是面板里的私聊、群聊默认模板）不在加载配置时检查是否存在：模板不存在时启动照常，日志有一条警告，运行时改用 `default`，面板的 bot 页标出“人设模板“x”已不存在，暂用 default”；在面板里建出这个模板后说明消失。数据库里没有 `default` 时用程序内置的默认人设。

**升级到面板聊天共用记忆、去掉面板接管的版本**（迁移 `0016_status_origin.sql`）：

- **去掉接管**：面板不能再往 QQ、Telegram 上的私聊发消息（返回 400），聊天页和 `GET /panel/api/chats` 只列面板里新建的聊天，不再有 `takeover` 字段。平台私聊里 bot 的消息一律发到原平台：升级前从面板往平台私聊发的消息，回复如果还在排队、等待或生成中，升级后会发给原平台上的用户。以前从面板发的消息留在平台私聊的记录里，消息页照样带“面板”标记；它们现在算作对方的消息，参与推算作息和“对方最后一条消息”。
- **共用记忆**：同一个 bot 在面板里新建的所有聊天共用一份记忆——摘要、对人的笔记和好感度、忘记时间、状态和状态条——翻找以前的聊天记录（recall 的关键词和语义检索）也在这个 bot 的所有面板聊天里查；平台上的私聊和群聊不变。迁移 0016 只给 `conversation_status` 加一列 `origin_conversation_key`（状态是在哪个聊天里设的，“回来说话”回到那个聊天），不改别的表。

按下面的步骤升级：

1. 停止从面板往平台私聊发消息，然后查还没送达的面板回复（只读查询，核心运行中也可以执行）：

   ```sh
   sqlite3 <data_dir>/conloom.db "SELECT conversation_key, status, COUNT(*) FROM outbox_actions
     WHERE channel = 'panel' AND status IN ('pending', 'leased') AND conversation_key NOT LIKE '%:panel:private:%'
     GROUP BY conversation_key, status;"
   ```

   有输出说明这些平台私聊里还有面板回复在等着送达，等一会儿再查，直到没有输出。回复还在生成时不在这张表里，所以发完最后一条消息后至少等一两分钟再查。
2. 备份（`conloom backup`），替换二进制，启动核心。
3. 第一次启动时，已有面板聊天的记忆一次性并入各自 bot 的共享范围。整个合并在一个事务里，最后在 `app_meta` 记下 `panel_memory_scoped`（合并时间），之后不再执行：
   - 一个 bot 只有一个面板聊天有记忆时，直接并入；
   - 多个聊天都有记忆时：摘要取最近整理过的那个；整理游标取各聊天里最大的，已经整理过的消息不会再整理一遍，其他聊天里落在这个游标之前、还没整理的消息也不再整理；每个人的笔记和好感度取最近更新的那一行，忘记时间取这个人在各聊天里最晚的那次（在哪个聊天里说过 /忘记我，合并后都照样算数，之前的聊天记录在所有面板聊天里都翻不出来）；修订记录和状态条的变化记录全部保留；状态取到期最晚的那个，它的“回来说话”留在设它的聊天里，其他聊天的“回来说话”取消；每条状态条取最近更新的值；
   - 语义检索的段仍按聊天存，不需要处理；
   - 平台私聊和群聊的数据不动。

   日志里每个合并过的 bot 有一条 `panel chat memory merged into the bot's shared scope`（`chats` 是合并的聊天数），审计里有一条 `memory.scope.migrate`，列出每个 bot 合并了几个聊天。

回退到这个版本以前：旧二进制忽略 `origin_conversation_key` 列，但它按每个聊天自己的键读写面板聊天的记忆，看不到合并后的共享记忆；它在面板聊天里新写的记忆，再升级回来时也不会再合并（合并只做一次）。要完整回退，用升级前的备份。

**升级到启动校验更严格的版本**（迁移 `0018_outbox_indexes.sql`；同一版本还改了 QQ 消息 ID 的格式，见下一段）：

- **启动校验**：下面这些配置以前能启动，但之后每次回复都会失败，现在 `serve` 启动时直接报错（退出码 78），升级前先检查：`bots[].id` 和 `bots[].platform` 里不能有 `:`（它是会话键的分隔符）；`model_profiles.*.endpoint` 必须是 `http://` 或 `https://` 开头的完整 URL；每个模型的 `context.reserved_output_tokens` 和 `context.safety_margin_tokens` 不能为负，两者之和必须小于 `context.configured_window`；`runtime.gpu_kv_cache_tokens` 必须是 0（不限）或不小于 `configured_window`；默认策略（`default_group_policy`、`default_private_policy`）的 `context.max_context_tokens` 必须是 0 或大于 `short_context` 的 `reserved_output_tokens + safety_margin_tokens`，通过 API 和面板修改策略时也按这条校验。另外 `panel.users[].name` 首尾不能有空白。
- **迁移 0018** 只给 `outbox_actions` 加两个索引（按会话找最近一次回复、按过期时间找没送达的动作），不改表结构，启动时自动执行；动作很多的库第一次启动要多花一点时间建索引。旧二进制不认识这个迁移，会照常在升级后的库上运行。
- **熔断指标**：`conloom_breaker_open` 改为每次抓取时按路由器的熔断状态现算，冷却过后就回到 0；以前要等这个角色的下一次模型调用才更新，面板概览会一直显示熔断。按这个指标设的告警会比以前更早恢复。

**升级到 bot 设置存在数据库里的版本**（迁移 `0019_bot_settings.sql`）：QQ、Telegram 机器人的显示名、管理员、私聊开放范围和私聊白名单、启用的群、私聊和群聊的默认人设模板、技能从配置文件挪到数据库，在 Web 管理面板里改（见 [配置参考：bots](configuration.md#bots)）；配置文件的 `bots[]` 只留 `id`、`platform`、`platform_self_id`。迁移只新增表 `bot_settings`，启动时自动执行。

1. 先备份（`conloom backup`）和 `conloom.yaml`，配置文件原样保留，替换二进制后启动核心。
2. 第一次启动时，只要 `bots[]` 里还写着 `name`、`admins`、`private_chat`、`enabled_conversations`、`persona_template`、`skills` 中的任何一个键，每个配置 bot 的设置就按旧版本的效果导入数据库：没写的键取旧版本的默认值（显示名 `织语`、私聊 `admins`、模板 `default`，其余为空）；`persona_template` 同时成为私聊和群聊的默认模板；`enabled_conversations` 里的群进启用的群，`private` 项进私聊白名单。数据库里已经有设置的 bot 跳过，以数据库为准。整个导入在一个事务里，最后在 `app_meta` 记下 `config_bot_settings_migrated`（迁移时间）。日志里每个导入或跳过的 bot 各有一条，审计里有一条 `bot.settings.migrate`（列出 `migrated` 和 `skipped`），最后一条警告提示删除这些键。导入前这些键照旧校验，`skills` 里的技能必须存在且工具齐全，不通过时启动失败（退出码 78），什么都不导入，改好再启动。
3. 确认 bot 照常工作（面板的 bot 列表，适配器用的 `GET /api/v1/capabilities` 返回的群和私聊开放范围和以前一样），然后从 `conloom.yaml` 的 `bots[]` 删掉这些键，重启。之后还写着其中任何一个键时启动失败，退出码 78（systemd 不会反复重启），报错列出哪个 bot 还写着哪些键，提示到面板里改。
4. 示例配置不再用 `CONLOOM_ADMIN_QQ`、`CONLOOM_GROUP_ID`、`CONLOOM_TG_ADMIN_ID`、`CONLOOM_TG_GROUP_ID`、`CONLOOM_TG_FRIEND_ID`，删掉这些键后可以从 `.env` 里删掉它们。

- 旧配置里所有 bot 都一个这样的键也没写（全用默认值）时，第一次启动不导入，配置 bot 用新的默认值：和旧版本相比只有显示名变了，从 `织语` 变成 bot 的 ID；要原来的名字就在面板里改。之后在配置文件里新加的 bot 也用新的默认值，不写入数据库，直到在面板里保存。
- 面板里保存后核心立即按新设置工作；适配器每 5 分钟从核心拉一次启用的群和私聊开放范围，最多 5 分钟后生效。
- 存在数据库里的技能不存在或缺少工具时不再让启动失败：这个技能不生效，日志有一条警告，面板的 bot 页标出原因。
- `conloom mock` 不带 `-user` 时取 bot 的第一个管理员：从配置的数据目录里的数据库读（数据库不存在时不新建），没有存过设置时用配置文件里还写着的 `admins`。

**回退到 bot 设置还在配置文件里的旧版本**（没有迁移 0019）：旧二进制忽略 `bot_settings` 表，只认配置文件里的设置。回退前把面板里的设置抄回 `bots[]`（旧版本一个 bot 只有一个 `persona_template`，私聊和群聊模板不同时只能选一个），否则 bot 用旧版本的默认值（显示名 `织语`，没有管理员和启用的群）。再升级回来时，配置里还写着这些键就会因为已经迁移过而启动失败，先删掉；在旧版本上改过的设置不会再导入，以数据库里的为准。

**升级到按会话区分 QQ 消息 ID 的版本**（没有表结构迁移，不需要额外步骤）：NapCat 的消息号只在一个会话里不重复，不同会话之间会撞号，以前撞号的消息会被核心当成重复消息丢掉。现在 NapCat 适配器把消息 ID 写成 `msg_<会话ID>_<消息号>`（例如 `msg_group_10001_7001`），幂等键和 ACK 的平台消息 ID 也带上会话。已经保存的 QQ 消息保留旧的 `msg_<消息号>`，不做转换，所以升级后群友引用升级前的消息时，核心找不到被引用的那条。判断要不要回复时按没有引用处理：引用机器人的旧消息不再算“回复机器人”（同时 @ 了机器人照常回复），在聊会话里引用别人的旧消息也不再让他离开会话；上下文里也不显示被引用的内容。观望加入挑选回复对象时则相反，引用了找不到的消息算作在回复别人，这条消息不会被选为加入的对象。升级后的消息不受影响。回退到旧版本时反过来，引用升级后消息的情况同样找不到被引用的那条。

**回退到人设模板还在配置文件里的旧版本**（没有迁移 0015）：旧二进制忽略 `app_meta` 表，库里的模板在旧版本上是“在线写入的模板”，照样生效；但旧版本要求配置 bot 的 `persona_template` 存在于配置文件，而且 `default` 以外引用的模板必须写回 `persona_templates`，否则启动失败。再升级回来时，配置里还有 `persona_templates` 就会因为已经迁移过而启动失败，先删掉这一段。

**回退到没有面板 bot 和在线人设模板的旧版本**（没有迁移 0013、0014）：旧二进制忽略这三张表，不需要用备份恢复。

- **配置**：先从 `conloom.yaml` 的人设模板里删掉 `appearance`、`appearance_image`、`appearance_image_skip`，否则旧二进制因为未知字段启动失败。`model_profiles.vision` 旧版本用不上，建议一并删掉（旧版本会把它的 `token_estimate_factor` 算进估算系数）。
- **面板 bot**：面板 bot 和它们的聊天留在库里，但旧版本没有面板 bot，这些聊天在旧版本上用不了；再升级回来后照常使用。
- **人设**：在线修改和新建的模板在旧版本上不生效，新会话用配置文件里的模板；人设版本里的外观字段被旧二进制忽略，回复不带图，它写入的新版本（API、面板、`/persona`）不带这些字段。再升级回来后在线模板和图片都还在。

**回退到没有 Web 管理面板的旧版本**（没有迁移 0008、0009）：先从 `conloom.yaml` 里删掉整个 `panel` 段和 `runtime.usage_retention`，否则旧二进制因为未知字段启动失败。旧二进制会忽略 `model_calls`、`panel_sessions` 两张表，不需要用备份恢复；回退期间的模型调用不会记入用量，再升级回来后这段时间在用量页上是空白。

**回退到没有面板聊天的旧版本**（没有迁移 0011，也就是 `messages`、`outbox_actions` 没有 `channel` 列的版本）：旧二进制不认识通道。面板里新建的聊天（`panel_…`）里还没送达的回复会被它当成普通回复，交给适配器（发不出去）。这包括发件箱里还没送达的动作，也包括还在排队、等待或生成中的任务——旧二进制启动时会恢复这些任务，写出的回复同样没有通道。按下面的步骤回退：

1. 停止新版本之前，先在面板里确认所有面板聊天都不再显示“对方正在输入…”，并且这段时间不要再从面板发消息。
2. 停止新版本，并且在启动旧版本之前，对数据库执行下面两条语句（先备份）：

   ```sql
   UPDATE tasks SET status='cancelled' WHERE status IN ('queued','running')
     AND conversation_key LIKE '%:panel:private:%';
   UPDATE outbox_actions SET status='cancelled' WHERE channel='panel' AND status IN ('pending','leased');
   ```
3. 启动旧版本。

旧二进制忽略 `channel` 列。面板里新建的聊天（平台为 `panel`）留在库里，但在旧版本上不能用：旧版本没有面板聊天，也没有对应的适配器，不要在旧版本上给这些会话发消息或触发主动发言。再升级回来后可以继续聊或删除。

**回退到没有记忆字数上限策略的旧版本**（没有 `memory.summary_max_chars`、`note_max_chars` 的版本）：没有表结构迁移，先从 `conloom.yaml` 里删掉这两个键（包括 `default_private_policy` 里的），否则旧二进制因为未知字段启动失败。旧二进制用它写死的较短上限（群聊 800 / 200 字、私聊 1200 / 500 字），下一次整理时会把更长的摘要和笔记截到这些上限；存库策略版本里的这两个键会被它忽略，它写入的新版本不带这两个键，再升级回来后按会话类型的默认值。

**回退到没有私聊节奏的旧版本**（没有迁移 0010，也就是没有分条发送和私聊等待的版本）：先从 `conloom.yaml` 里删掉 `reply.max_parts`、`reply.private_wait`、`reply.private_max_wait`（包括 `default_private_policy` 里的），否则旧二进制因为未知字段启动失败。旧二进制忽略 `outbox_actions` 的 `not_before` 列，回退时还没发出的分条会立即接连发出；已取消的动作它不会发放，会随所属任务一起被清理。存库的策略版本里这三个键会被旧二进制忽略，它写入的新版本（API、面板、`/pause` 等）不带这三个键，再升级回来后按默认值补齐，`max_parts` 变回默认策略里的值。

**回退到没有长期检索的旧版本**（没有迁移 0006、0007，也就是没有关键词索引和语义检索的版本）：
- **配置**：换回旧二进制之前，先从 `conloom.yaml` 里删掉 `runtime.ops_retention`、`runtime.audit_retention` 和整个 `embedding` 段。旧版本严格解析配置，遇到不认识的字段会直接启动失败。
- **表结构**：旧二进制会忽略 0006 和 0007 新增的表，不需要用备份恢复。
  - 0006：`message_fts`、`message_fts_progress`。回退期间写入的消息不会进关键词索引，再升级回来后由后台补建自动补上（见 [架构 5.7 节](architecture.md#57-长期回忆的检索)）。
  - 0007：`memory_chunks` 等四张表，见下一段。
- **“忘记”**：回退期间有人执行了 `/忘记我`，或者通过 API 删除了群友记忆：
  - 关键词索引不用处理，recall 是在查询时按忘记时间过滤的；
  - 语义检索的段由新版本启动时的自动对账补删，见下一段。

**回退到没有语义检索的旧版本**：迁移 `0007_memory_chunks.sql` 只新增四张表，旧二进制会忽略它们，配置里要先删掉 `embedding` 段。旧二进制处理 `/忘记我`（或通过 API 删除群友记忆）时不会删除这些表里的段；再升级回来后，核心启动时会自动对账：按每个被忘记成员的忘记时间补删对应的段并排队重建（见 [架构 5.7 节](architecture.md#57-长期回忆的检索)），不需要手动处理。想彻底从头重新生成的话，也可以在升级回来之前（核心停止时）用 `sqlite3` 清空这些表，这一步可选：

```sh
sqlite3 <data_dir>/conloom.db 'DELETE FROM memory_chunks; DELETE FROM memory_chunk_state; DELETE FROM memory_chunk_rebuild; DELETE FROM memory_chunk_forgets;'
```

**回退到没有记忆层的旧版本**：记忆层的迁移（`0005_memory.sql`）只新增三张表，旧二进制可以直接在升级后的库上运行，会忽略这些表；要完全回到升级前的状态，仍然用升级前的备份。回退时注意：

- 不要在 `conloom.yaml` 里写 `memory` 段（包括 `default_group_policy.memory`），否则旧二进制会因为未知字段启动失败。回退到没有私聊记忆的版本时同理，要去掉 `default_private_policy` 段和 `memory.consolidate_min`。
- 旧二进制运行期间写入的新策略版本（群策略 API、`/pause`、`/resume`、`/主动 全群` 等）会丢掉 `memory` 段。再升级回来以后，这些群的记忆会变成关闭，需要重新开启；已有的摘要、笔记和好感度不会丢失，重新开启后按“关闭与重新开启”的规则继续整理（积压超过 `memory.backlog` 条时只整理最近的这么多条）。

## 监控

- **健康检查**：`GET /healthz` 查一次数据库，正常返回 `ok`，失败返回 503。
- **日志**：`log/slog` 结构化日志写到 stderr（systemd 下进 journald），格式和级别见 [配置参考](configuration.md#observability)。
- **指标**：`GET /metrics` 提供 Prometheus 指标，全部以 `conloom_` 开头，包括事件和任务计数、模型延迟和 token 用量、准入排队、发件箱投递、审查结果、熔断状态，以及群聊参与相关的两项：

| 指标 | 标签 | 说明 |
|---|---|---|
| `conloom_continuation_exits_total` | `reason`：`rule`、`model`、`limit`、`idle` | 在聊模式的退出次数：`rule` 为参与者规则退出，`model` 为连续跳过使会话结束，`limit` 为未点名回复达到上限使会话结束，`idle` 为超过保持时间无活动而结束 |
| `conloom_proactive_total` | `result`：`evaluated`、`sent`、`skipped` | 加入群聊：`evaluated` 为通过检查、交给模型评估，`sent` 为实际发出，`skipped` 为模型放弃，或调用模型前 / 发出前重新检查时已不允许发出 |
| `conloom_wake_total` | `kind`：`status_return`、`initiate`、`follow_up`；`result`：`sent`、`skipped`、`dropped`、`postponed`、`missed`、`cancelled` | 私聊里机器人自己发起的一轮：`sent` 为实际发出，`skipped` 为模型放弃、审查拦下、出错或发出前重新检查时已不允许，`cancelled` 为对方开口、`/stop` 等取消；`dropped`、`postponed`、`missed` 记定时到点时的丢弃、顺延和错过 |

配置了 `panel` 段时，也可以在 [Web 管理面板](panel.md) 的概览页看实时状态、在用量页看按天统计的模型调用和 token（这部分存在库里，重启不丢）。面板和用量记录新增的指标见 [Web 管理面板：指标](panel.md#指标)。

完整的指标列表、日志里的原因代码（如 `join skipped` 的 `few_messages`、`cooldown`、`no_eligible`）见 [架构 8 节](architecture.md#8-可观测性)。停顿检查没有加入时的日志带窗口消息数和目标资格人数，不记录兴趣词、消息正文和群友昵称。指标在进程重启后归零。

## 隐私

- 日志默认**不记录**消息正文，只记 ID、长度、会话、路由和耗时；本地调试时可以设 `observability.log_message_content: true`。
- 只有启用的群（以及按私聊开放范围放行的私聊）的消息会被保存。其他会话的消息不转发、正文不落库，只留一条不含正文的幂等记录（幂等键、请求哈希和响应），按 `idempotency_retention` 清理。
- 消息保存在本机 `<data_dir>/conloom.db` 中，用作对话上下文；被处理的事件原文（含正文）在幂等记录里保留 `idempotency_retention`。请像对待聊天记录一样保护数据目录和备份文件。
- 配置了 `embedding` 时，`recall` 的查询句（模型写的 `query`）会原样发给向量服务，不做打码或其他脱敏。
- 配置了 `embedding` 时，段落文本会发给向量服务：它和 `recall` 能看到的内容一样（命令、被忘记的内容和被审查拦截的消息不收，联系方式打码，不带 ID），但仍然是聊天原文，所以向量服务应当部署在你自己控制的机器上，并像上文那样只通过隧道或内网访问。库里只存向量和文本的哈希，不另存段落文本。
- 主动搭话的兴趣画像（每位 @ 过机器人的群友最多 50 个关键词及权重）、档位和最近一次 @ 机器人的时间保存在 `conloom.db` 中，不写入日志，也不会出现在群消息里；加入群聊时只把在场目标资格人合并后的前 5 个兴趣词放进发给模型的提示。在聊会话和停顿计时器只在内存里，不落库。
- 组装好的上下文会发送到你配置的模型 endpoint，也就是说消息内容会离开本机（除非模型部署在本机）。
- 模型输出会剥离平台控制码，并对密钥形态的字符串和配置中的任何密钥打码后才发送。
- Web 管理面板能看到全部会话的聊天记录、记忆和配置（密钥打码），登录密码要和数据目录一样保管好；不要让面板未经 HTTPS 暴露在公网。用量明细只记 token 数、耗时和结果，不记提示词和回复内容。
