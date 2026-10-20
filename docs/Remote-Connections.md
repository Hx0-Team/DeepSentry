# 远程连接与代理兼容性

DeepSentry 2.0.6 支持 SSH、SFTP、FTP/FTPS、Telnet 和原生 TCP/SOCKS5/HTTP 转发。本文说明协议兼容及使用边界。

## SSH 的具体修正

原有兼容列表已经包含 ssh-rsa、DSA、DH-SHA1、AES-128-CBC、3DES，但底层 Go SSH 实现不包含 AES-192/256-CBC 和 HMAC-MD5。向配置填写算法名字不能让库自动实现它，因此仅提供这些算法的旧 SSH2 服务仍会失败。SHA1 GEX 的固定最小组 2048 位也会排除旧 1024 位组。

对固定版本 golang.org/x/crypto v0.57.0 做四个文件的明确补丁：CBC 使用标准 AES 与已有 SSH CBC 数据包实现，增加 AES-192/256-CBC；MAC 使用标准 HMAC-MD5 实现，增加 MD5/MD5-96；只在 SHA1 GEX 中允许 1024 位最小组，并使请求、验证和交换摘要使用同一个实际最小值。SHA256 GEX 仍保留 2048 位最小组。

现代算法优先，弱算法只在 ssh_legacy_compat 开启时进入应用协商列表。false 模式现在显式使用 SupportedAlgorithms，避免 Go 默认列表仍包含部分旧 DH/SHA1-96。现代模式 RSA 用户签名也限制为 SHA2，DSA 私钥需要兼容模式。不修改全局系统 ssh_config、不关闭 known_hosts 身份核验，不修改 FIPS 限制。

另外：密码认证优先于 keyboard-interactive，减少旧服务器 MaxAuthTries=1 提前拒绝正确密码；私钥支持 ~/ 展开及加密口令；错误区分协商类别/对端算法/当前兼容模式/认证/SSH1，probe_ssh 返回实际服务版本、KEX、主机密钥、cipher、MAC。

旧算法与主机身份认证是不同环节，官方说明见 [OpenSSH legacy options](https://www.openssh.org/legacy.html)；类型化协商错误和算法接口见 [Go SSH API](https://pkg.go.dev/golang.org/x/crypto/ssh)。本项目使用固定 v0.57.0 加明确本地补丁，不能用网页最新版本替代本次源码。补丁来源与 SHA256、BSD 许可保留于 third_party/xcrypto/DEEPSENTRY-PATCH.json 和 LICENSE，发布包附带许可与说明；升级此依赖必须重新应用补丁并复验。

## 实际协议验证

| 受限 SSH2 服务配置 | 结果 |
| --- | --- |
| group1-SHA1 / AES-128-CBC / HMAC-SHA1 / RSA | 握手认证与 60,000 字节加密文件往返通过 |
| group1-SHA1 / AES-192-CBC / HMAC-MD5-96 / DSA | 同上通过 |
| group1-SHA1 / AES-256-CBC / HMAC-MD5 / RSA | 同上通过 |
| group14-SHA1 / 3DES-CBC / HMAC-SHA1-96 / RSA | 同上通过 |
| GEX-SHA1 / AES-256-CBC / HMAC-MD5 / RSA | 同上通过 |
| GEX-SHA256 / AES-256-CTR / HMAC-SHA256 | 同上通过 |
| Curve25519 / AES-GCM / RSA-SHA2 | 同上通过 |

这 7 组是本机运行的 Go SSH2 协议服务，限制了服务端真实协商与加密算法；不是历史 OpenSSH/Dropbear 二进制。补丁库还完成 1024 位 SHA1 GEX 双端交换摘要/派生密钥一致性测试、原有 SSH 单元测试和 race 检查。主机密钥匹配/不匹配、RSA-SHA1 用户签名，以及关闭兼容时拒绝旧服务均有检查。

**独立实现复验：**系统 OpenSSH_10.3p1（服务 banner SSH-2.0-OpenSSH_10.3），在临时目录和 loopback 端口启动临时服务，仅允许临时公钥及 SFTP；配置强制 group1-SHA1、AES-256-CBC、HMAC-MD5、ssh-rsa。生产客户端使用 strict known_hosts，成功认证并完成 80,000 字节 SFTP 文件写入/回读一致性检查。该结果证明跨实现兼容，不是“实测某个历史版本”。临时服务和密钥随测试退出清理，未改系统 SSH 服务设置。

复验命令和已知限制见 [验证说明](Verification-2.0.6.md)。

## SFTP 文件服务

- 新增 target_protocol=sftp 与 Fleet protocol=sftp，复用 SSH 认证/主机密钥/代理设置。向导能正确保存 SFTP 和加密私钥口令，系统指纹明确文件模式，避免反复尝试 uname/bash。
- SFTP-only 账号可以认证后直接打开子系统，不需要 Shell、PTY 或 SFTP 外的额外权限。测试验证没有请求 shell/exec/pty，100,000 字节二进制与中文/空格路径上传、读取、目录属性和下载全部一致。
- 普通 SSH 改为先检查 Shell，文件工具按需初始化 SFTP；缺少 SFTP 不再被直接认作网络设备。子系统初始化有界，缺失时 upload/download 明确报错，消除 nil 空指针。
- 文件操作和传输有界超时；超时关闭相关连接，不重放可能已写入的操作。旧服务使用顺序传输请求。下载先写临时文件，完整结束再替换目的文件；已有目的文件不因中途失败被截断。

```yaml
target_protocol: sftp
ssh_host: YOUR_HOST:22
ssh_user: YOUR_USER
ssh_key_path: ~/.ssh/id_rsa
ssh_key_passphrase: "" # 加密私钥按需填写，保持凭据文件私有
ssh_host_key_policy: accept-new
ssh_known_hosts_path: ~/.deepsentry/known_hosts
ssh_legacy_compat: true
```

## FTP/FTPS、Telnet 和转发

| 能力 | 优化与验证 |
| --- | --- |
| FTP/FTPS | 控制和数据连接使用同一捕获的代理配置；修改全局代理不会把已有 FTP 数据请求误导到另一条路由。stop/Close 不等待命令互斥锁，关闭控制、数据、主动监听资源；TLS 先断底层连接。完成的数据资源回收。既有 EPSV→PASV、EPRT→PORT、NAT、IPv6、显式/隐式 FTPS 和证书校验保持。新增实测使用 Go FTP 协议服务；未额外执行独立外部 FTPS 服务器测试。 |
| Telnet | IAC/子协商解析状态跨 TCP 分片与读取超时保留；支持 echo/SGA、TTYPE=xterm、NAWS=120×30；拒绝确认不循环回复。协商和命令写入有超时，明确单行输入。分片超时、终端类型、拒绝循环及已有设备登录/分页测试通过。 |
| TCP 映射、SOCKS5、HTTP CONNECT | 转发 EOF 正确 half-close，保留等到请求发送结束才产生的最终回复；CONNECT 使用 buffered reader，握手同包携带数据不丢。stop 取消待连接并关闭现有资源，流量计数随传输更新。三类转发均验证 FINAL:payload 全部到达。 |
| HTTP 代理 | 使用独立 transport，不隐式继承环境代理而产生回环；原始 HTTP 上游连接纳入资源管理，stop 可中止；设置响应头/握手/每主机连接上限，保留代理认证及请求头清理。 |

这些是现有进程内 TCP 映射/代理能力的可靠性改进，仍按进程生命周期管理；没有新增 NPS 服务端注册、反连控制面或系统持久化服务。

## 验收边界

全量 Go 测试、静态检查、相关模块 race、固定补丁库 SSH race 与跨平台编译/发布包校验分别留存。不同服务器与设备可能仅提供未实现的算法；SSH1-only、FIPS 禁止的旧算法或独立主机身份变更会给出明确原因，不通过跳过校验冒充修复。旧 SHA1/MD5/CBC 不应在现代模式被优先选用。
