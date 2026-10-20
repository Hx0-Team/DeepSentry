# DeepSentry 2.0.6 验证说明

发布验证日期：2026-10-20。环境为 macOS arm64、Go 1.26.9。自动化验证覆盖生产代码、内置工具、协议、配置、文档结构和 TUI 渲染；平台构建通过不代表每个系统的真实桌面交互均已验收。

## 本地复验

```sh
go mod verify
test -z "$(gofmt -l cmd internal)"
go vet ./cmd/... ./internal/...
go test -count=1 -shuffle=on ./cmd/... ./internal/...
go test -race -count=1 ./cmd/... ./internal/...
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./cmd/... ./internal/...
go run github.com/securego/gosec/v2/cmd/gosec@v2.28.0 -severity high ./cmd/... ./internal/...
node scripts/verify-hawkeye-107.mjs
bash build.sh
(cd build && shasum -c SHA256SUMS)
./build/deepsentry --version
./build/deepsentry --computer-check
```

- HTTP 与工具调度覆盖真实 loopback 服务、取消、限流、依赖屏障和执行器隔离。
- SSH/SFTP、FTP、Telnet 与代理转发使用隔离测试服务；SSH 兼容开关、主机身份校验与文件传输均有回归。代理关闭另有并发关闭完成屏障测试，相关竞态用例重复 30 次通过。
- Office/PDF 测试覆盖真实文件结构回读、中文、表格、公式、PPT 备注、尺寸限制和拒绝覆盖。
- HawkEye 检查启动随包 MCP Server，验证初始化、四种工具配置和无效参数拒绝。浏览器扩展现场操作需在已连接的浏览器中单独验收。
- TUI 测试覆盖深浅主题、窄屏、余额响应隔离、Markdown 和代码复制。

## 外部服务与平台边界

默认测试不访问付费模型服务。DeepSeek 三协议和 Search 的真实 API 检查为显式开启项，详见 [DeepSeek 适配](DeepSeek-适配.md)。模型用量以服务返回的 usage 为准；上下文估算不等于计费数据。本版本不承诺固定的 token 节省率或整体提速比例。

Windows/Linux 桌面、管理员窗口、第三方自绘控件和旧版 macOS 仍需目标真机验证。Linux 依赖 X11。macOS helper 为临时签名，需要系统屏幕录制和辅助功能权限；生产分发的 Developer ID 签名与公证由部署方完成。`--computer-check` 仅检查依赖与权限，不发送键鼠输入。

ZIP 按公开 SHA256 清单生成，包含七个平台程序、macOS helper（在 macOS 构建时）、示例配置、内置 Skills、使用说明和 2.0.6 海报。个人配置、API 密钥、运行报告、历史验收记录和本机发布材料不在清单中。
