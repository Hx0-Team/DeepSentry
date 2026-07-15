---
name: awd-plus
description: AWD-plus 综合运营工作流 — 多服务态势、低风险自动化检查、证据链汇总和处置队列
license: Apache-2.0
---

# AWD-plus Skill

## 何时使用

- 用户提到 AWD-plus、多服务、多队伍、多目标防守、态势运营、比赛值守。
- 需要把服务可用性、日志、文件、网络连接和 flag 暴露统一成证据链。

## 工作方式

1. 先用 `todo` 建立 3-7 项检查清单，避免遗漏服务、日志和文件线索。
2. 多目标环境先 `fleet_inventory`，再按 selector 分批检查。
3. 服务面使用 `awd_service_check`，主机面使用 `target_health_summary`、`port_listen`、`net_connections`。
4. Web 面使用 `headless_browser`、`web_snapshot`、`webshell-hunter`。
5. 证据面使用 `flag_scan`、`read_log`、`read_gzip`、`proc_socket_map`。
6. 所有修复、上传、批量执行和脚本运行都必须等待用户确认。

## 推荐委派

```json
{"action":"task","task_name":"awd-defender","task_prompt":"检查当前目标 Web 服务可用性、Webshell 和日志异常"}
{"action":"task","task_name":"network-analyst","task_prompt":"分析异常连接和可疑出站流量"}
```

## 输出格式

```text
## AWD-plus 态势报告
### 当前态势
### 服务/目标矩阵
### 证据链
### 风险排序
### 待确认处置队列
```

## 边界

- 聚焦防守运营，不做自动攻击编排。
- 保留证据优先；删除、覆盖、重启服务前必须说明影响并请求确认。
