# XDial for Windows

基于共享 Go 控制面（`core/config`、`core/libbox`、`sing-box`、`sslcon`、`tailscale`）与 **Wails v3 + React / TypeScript** 构建的 Windows 原生体验客户端。

## 功能概览

- **系统托盘常驻 + 拨号盘小窗（Flyout, 360px）**：
  - 64×64 超采样抗锯齿动态拨号盘托盘图标（未连接、12 帧脉冲连接中、已连接、仅 SOCKS5 咬口、异常叹号状态，自适应浅色/深色任务栏）。
  - 复刻 macOS 石板蓝灰仪表盘与物理手感 6 孔可旋转拨号盘（支持真实孔位拖拽回弹与连接中自动拨号动效）。
  - 实时上下行网速胶囊、连接时长、事务步骤（`ConnectionPlan`）展开、出口 IP 观察与 `RuleSet → Line` 分流详情。
- **独立设置窗口（810×565）**：
  - 多配置来源（Profile Library Schema 3）切换、重命名、导出与删除；支持导入 XDial / `sing-box` JSON、Clash YAML、Surge 配置、Base64 节点链接以及 HTTPS 在线订阅。
  - 线路（`Direct`、`AnyConnect`、`VLESS + Reality`、`AnyTLS`、`Trojan`、`Shadowsocks`、`VMess`、`Tailscale` 及支持 VLESS + Reality / Shadowsocks 2022 的「我的云主机…」SSH 一键部署向导）、线路组（自动测速 URLTest / 固定选择 Selector + 并发测速）、规则（域名/CIDR、远程规则集、Windows `.exe` 进程匹配与反向开关）、场景（23 语义图标自动匹配与选择器、Wi-Fi SSID 自动激活、拖拽排序）。
- **三种 Windows 数据面接入模式**：
  - **接管系统 · 代理（默认，免管理员）**：开放本机混合 HTTP/SOCKS5 入口并自动配置 Windows WinINET 系统代理，含异常退出恢复日志。
  - **接管系统 · TUN（需管理员 + `wintun.dll`）**：通过 `sing-box` Wintun 虚拟网卡接管全系统 TCP/UDP/DNS，绑定启动前默认物理接口（Underlay）。
  - **仅 SOCKS5（免管理员）**：仅监听 `127.0.0.1:<port>`（默认 `5181`），不改动系统代理（暮紫 `ProxyOnly` 语义）。
  - 本地配置与凭据采用 **Windows DPAPI (`CryptProtectData`)** 绑定当前登录账户加密存储。

## 在家走 AnyConnect → Reality

1. 在「线路」中配置并启用 AnyConnect 和 VLESS + Reality 两条线路。AnyConnect 需要能访问 Reality 服务器地址和端口。
2. 打开「设置 → 线路组 → 添加链式代理」，第一跳选 AnyConnect，出口选 Reality，保存为「在家链路」。线路组的「自动选择 / 固定选择」只会选一条线路，不能代替链式代理。
3. 在「场景」中把「在家」的默认出口设为「在家链路」，选中场景并连接；已连接时重新应用。若只想让一部分流量走链，保留原默认出口，仅把对应规则绑定到链。若需随家庭 Wi-Fi 切换，再为场景设置 SSID 并启用自动切换。

系统代理、仅 SOCKS5、Wintun 共用这条链。匹配链路的请求从 Reality 出口发出；原有场景规则仍优先于默认出口。断开首跳不会自动直连 Reality。

## 构建与运行

### 一键构建单文件 `XDial.exe`

在 PowerShell 中执行（需要已安装 Git for Windows、Go 1.24+ 自动拉取 `go1.26.5`、Node.js 20+）：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build-windows.ps1
```

产物输出至 `build\windows\XDial.exe`。

- 启动常驻托盘并弹出拨号盘：`.\build\windows\XDial.exe`
- 直接打开设置窗口：`.\build\windows\XDial.exe --settings`
- 静默常驻托盘（开机自启模式）：`.\build\windows\XDial.exe --minimized`

*(如需启用 TUN 模式，请从 [wintun.net](https://www.wintun.net/) 下载对应架构的 `wintun.dll` 放置在 `XDial.exe` 同级目录，并以管理员身份运行。)*

### 运行单元测试与集成测试

```powershell
cd windows
$env:GOTOOLCHAIN = "auto"
$env:GOWORK = (Resolve-Path ..\build\patched-go\xdial-windows.work).Path
go test -v -tags "with_gvisor,with_utls" ./internal/...
```
