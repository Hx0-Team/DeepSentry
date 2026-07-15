---
name: awd
description: AWD 防守工作流 — 服务可用性、Webshell 排查、日志审计、flag 线索扫描与修复建议
license: Apache-2.0
---

# AWD Skill

## 何时使用

- 用户提到 AWD、攻防赛、防守、靶机服务保活、被打、Webshell、flag 泄露。
- 需要在比赛授权目标上做低风险巡检、证据整理和处置建议。

## 防守优先级

1. 服务可用性：用 `awd_service_check` 检查 HTTP/TCP 状态。
2. 暴露面：用 `port_listen`、`net_connections`、`process_list` 建立基线。
3. Webshell：委派 `webshell-hunter`，结合 `file_ident`、`file_strings`、最近修改文件。
4. 日志证据：委派 `log-analyst`，读取 Web/auth/syslog 最近异常。
5. Flag/敏感线索：用 `flag_scan` 找明文 flag、token、异常备份。
6. 修复动作必须确认：改文件、上传、批量命令、脚本、转发都不能自动执行。

## 推荐工具

```json
{"action":"tool","tool_name":"awd_service_check","tool_args":{"targets":"http://127.0.0.1:8080,127.0.0.1:22","timeout":"3"}}
{"action":"tool","tool_name":"flag_scan","tool_args":{"root":"/var/www","limit":"100"}}
{"action":"task","task_name":"webshell-hunter","task_prompt":"检查 /var/www 下可疑 Webshell，不要删除文件"}
```

## 输出格式

```text
## AWD 防守报告
### 服务状态
### 高风险发现
### 入侵/后门证据
### Flag/敏感信息暴露
### 建议处置队列
```

## 边界

- 不自动攻击其他队伍，不批量利用漏洞。
- 不直接删除证据文件；先建议隔离/备份路径。
