---
name: ctf
description: CTF 解题工作流 — Web/文件/日志/流量线索分析、flag_scan、headless_browser 与证据化输出
license: Apache-2.0
---

# CTF Skill

## 何时使用

- 用户明确提到 CTF、flag、题目附件、Web 题、Misc、取证、日志分析。
- 需要在授权靶场或本地题目环境中查找 flag 或解释解题路径。

## 工作流

1. 明确题型、输入材料和授权范围；不要扫描第三方公网目标。
2. Web 题优先使用 `headless_browser` 获取渲染后 DOM、表单、链接和文本；失败时接受静态回退。
3. 文件/目录题优先 `glob`、`grep`、`file_ident`、`file_strings`、`flag_scan`。
4. 日志/取证题优先 `read_log`、`read_gzip`、`log-analyst` 子 Agent。
5. 每个结论必须绑定证据：路径、URL、匹配片段、响应状态或日志时间。

## 推荐工具

```json
{"action":"tool","tool_name":"tool_catalog","tool_args":{"category":"Web探测","query":"headless"}}
{"action":"tool","tool_name":"headless_browser","tool_args":{"url":"http://127.0.0.1:8080","mode":"snapshot","wait_ms":"1500"}}
{"action":"tool","tool_name":"flag_scan","tool_args":{"root":".","limit":"80"}}
```

## 输出格式

```text
## CTF 分析结果
### 题型判断
### 关键证据
### 已尝试路径
### Flag / 当前最佳结论
### 下一步
```

## 边界

- 不自动爆破真实账号，不做未授权公网攻击。
- 需要写脚本时先说明脚本目的、输入、输出和读写边界，再用 `script_run` 请求确认。
