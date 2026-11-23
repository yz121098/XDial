# 添加我的云主机

用户在自己的 Linux 服务器上部署一个代理，保存为一条普通线路。iPhone、Mac（`apple/CloudHost`）与 Windows（`CloudHostWizardModal`）共用同一套 Go 核心
`core/cloudhost`（经 Libbox 的 `CloudHostSetup` 调用），SSH、检查、部署和回滚都不依赖桌面 shell。
支持两种无需自有域名与证书的协议：
- **VLESS + Reality（默认推荐）**：`vless` + `xtls-rprx-vision` + X25519 Reality 握手伪装（默认伪装域名 `www.microsoft.com`，支持自定义域名与空闲的 `443` 或 `1024–65535` 端口，原生通过 XUDP 承载 UDP）。
- **Shadowsocks 2022**：`2022-blake3-aes-128-gcm`（端口 `1024–65535`，同时监听 TCP 和 UDP）。

## 流程

1. **核对指纹**：只做密钥交换，在发送任何凭据之前显示服务器的主机密钥指纹（OpenSSH 的 `SHA256:` 形式），并提示与
   服务器上哪个文件比对（`ssh-keygen -lf /etc/ssh/ssh_host_*_key.pub`）。扫描和登录使用同一主机密钥算法顺序
   （ed25519 优先），登录时指纹不符直接拒绝。
2. **只读检查**：判断权限（root、免密 sudo 或用登录密码 sudo），读取系统、架构、systemd、apt、已有 sing-box 和服务、
   本工具以前留下的代理、ufw/firewalld 状态和已监听端口。阻断条件：非 Ubuntu 20.04+ / Debian 12+、非 amd64/arm64、
   没有 systemd、没有 root 或 sudo、无法读取的同名服务。
3. **改动清单**：按所选协议、伪装域名与端口列出每一项改动，用户确认后才执行。端口不能被占用；Shadowsocks 2022 需在 `1024–65535`，VLESS + Reality 额外允许空闲的 `443`。
   改协议、伪装域名或端口都会立即作废旧清单，重新生成之前、以及生成期间都不能部署；只有与当前选项一致的最新清单才能确认。
4. **部署**：任何一步失败或用户取消，都按相反顺序撤销本次做过的改动；再次运行时复用已有代理，或按新端口改配置。
5. **验证**：在 App 内建一个只含这条线路的临时 sing-box 会话，经它发出三项 HTTPS 请求，不改动当前连接：
   `www.gstatic.com/generate_204`（连通，几十字节）、`1.1.1.1/cdn-cgi/trace`（出口 IP，读取上限 64 KB，实际不到 1 KB）、
   `speed.cloudflare.com/__down?bytes=10000000`（测速，最多 10 MB、最长 12 秒）。这些服务看到服务器的 IP、请求时间和地址，
   看不到 SSH 账号、密码或线路密钥；测速同时用掉服务器和设备的流量。部署前的检查页和数据使用说明都写明了这些
   （`apple/CloudHost/CloudHostDisclosure.swift`，`core/libbox/cloud_host_test.go` 锁定端点和数据量）。
   HTTPS 不通时提示在服务商的安全组放行端口，可重新测试，期间不能保存。
6. **保存**：通过 iPhone 的钥匙串保管库 / Mac 的加密配置库保存为普通 Shadowsocks 线路，不加入任何场景。

## 服务器上的改动范围

| 资源 | 位置 | 何时创建 | 回滚 |
|---|---|---|---|
| sing-box 签名密钥 | `/etc/apt/keyrings/xdial-sagernet.asc` | 没有 sing-box 且没有已有 sagernet 源 | 删除 |
| sing-box 软件源 | `/etc/apt/sources.list.d/xdial-sagernet.sources` | 同上 | 删除 |
| sing-box 软件包 | apt 安装 | 服务器上没有 sing-box | 本次安装的才 `purge` |
| 代理配置 | `/etc/xdial-proxy/config.json`（0600） | 每次 | 新建的删除；改端口时恢复原配置并重启 |
| 系统服务 | `/etc/systemd/system/xdial-proxy.service` | 首次 | 停止并删除 |
| ufw 规则 | 仅代理端口的 TCP/UDP | ufw 已启用 | 删除这两条 |
| 状态记录 | `/etc/xdial-proxy/state.json` | 成功后 | — |

签名密钥随 App 发布（`core/cloudhost/sagernet.asc`，Project S，指纹 `2C31 7FBD 5D88 6B4E 89BA E8DA 6D91 5217 2A2B 2F0C`），
服务器不在线下载密钥，apt 按它校验软件包。`apt-get update` 只刷新 sing-box 这一个源。
不修改 SSH、默认路由、已有的 sing-box 服务和 firewalld，不关闭防火墙。服务启动后确认监听端口的是这个服务自己的进程，
并且没有重启，才算部署成功。

## 中断与恢复

App 在线时，任何一步失败或用户取消都会当场倒序撤销。进程被杀、断网或 SSH 中断时无法当场撤销，所以每完成一步，
都会在 `/etc/xdial-proxy/state.json` 里记下 `in_progress` 和本次已创建的资源（安装前先记下软件包）。撤销没能完成时，
App 如实提示改动可能留在服务器上；下次连接这台服务器时，检查会发现这条记录，改动清单的第一步就是先撤销上次留下的
改动（必要时先 `dpkg --configure -a` 再移除只装了一半的 sing-box），然后重新设置。这种恢复不是即时的，要等用户再次打开向导。

## 凭据

密码和私钥只在本次向导的内存里使用：sudo 密码通过 stdin 传给 `sudo -S`，文件内容也经 stdin 写入，都不出现在命令行；
错误信息只给原因，不回显服务器输出。关闭向导或保存后清空。SSH 凭据和主机指纹都不保存。

## 测试

- `go test ./core/cloudhost/`：检查与改动清单的判断、端口、复用、指纹确认、凭据不进命令行。
- `scripts/test-cloudhost-e2e.sh`：在隔离容器（真实 systemd、sshd、apt）里端到端验证 Ubuntu 24.04/22.04/20.04、
  Debian 12，`XDIAL_E2E_PLATFORM=linux/amd64` 验证另一种架构。覆盖指纹变化、注入、无权限账号、端口占用、
  失败回滚、取消回滚、部署、经代理的 HTTPS 与测速、重复运行复用、改端口、免密 sudo、root 私钥登录。不连接真实云主机。
- iOS `CloudHostSetupTests`：解析 Go 返回、进度、改端口作废旧清单、只有验证通过才可保存、关闭时清空凭据。
- Mac 的向导截图：`XDial Debug --render-settings-preview <目录>` 额外输出 `cloud-host-*.png`。

## 限制

- 只显示这次实际测得的结果，不承诺在任何网络环境下长期稳定。
- 服务商的安全组无法由 XDial 修改；firewalld 需要用户自行放行。
- 本版未覆盖 Debian 11（测试镜像所用的软件源目前无法安装依赖，未验证），检查时会直接说明不支持。
- 只在隔离的容器里验证过，还没有在真实云主机和真机上测试。
- 还没有删除服务器上代理的入口；`/etc/xdial-proxy/state.json` 记录了可以安全移除的资源。
