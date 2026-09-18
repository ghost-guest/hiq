# 运维求助指引 — 搜索链接速查

遇到问题时的查找顺序建议：**先查本地（读表/规格/审计）→ 再查厂商官方 → 最后社区**。
下面 `{kw}` 处替换成你的关键词（命令名、告警号、报错原文、协议名…）。

---

## 0. 先查我们自己

| 查什么 | 去哪 |
|---|---|
| 某命令为什么被拒绝 / 读表范围 | 设置 → 运维 → 读表扩展；`docs/NETDEV_SPEC.md` 附录 B-1 |
| 安全边界与护栏语义 | `docs/NETDEV_SPEC.md` §6-§8；`[netdev.guardrails]` 注释 |
| 某次操作的完整记录 | 设置 → 运维 → 最近审计（guardrail/write/read 分类齐全） |

## 1. 命令语法 / 是否只读（加读表前先查证）

| 厂商 | 链接 | 说明 |
|---|---|---|
| 华为 | https://info.support.huawei.com/info-finder/tool/zh/enterprise/commands | Info-Finder 命令查询（按产品/版本；含告警、日志、MIB） |
| 华为文档 | https://support.huawei.com/enterprise/zh/search?keyword={kw} | 企业支持站全文搜索 |
| Cisco | https://www.cisco.com/site/us/en/support/index.html | Support 首页检索 → 选产品的 Command Reference |
| Cisco 文档 | https://www.cisco.com/c/search/index.html#q={kw} | 全站文档搜索 |
| 中兴 | https://support.zte.com.cn | 技术支持站内检索产品手册（命令参考分册） |

> 官方查询工具都注明"以产品文档为准"——与我们的原则一致：**查询是入口，
> 落进 [netdev.extra_read] 才生效**。

## 2. 告警 / 日志 / 错误码解读

| 需求 | 去哪 |
|---|---|
| 华为告警/日志含义 | Info-Finder（同上）的"告警/日志"页签，输入告警号（如 OSPF_1.3.6.1.2.1.14.16.2.6） |
| Cisco 错误信息 | cisco.com 搜 "Error Message Decoder" + 产品名；或直接把 `%XXX-…` 原文贴进 Support 搜索 |
| 中兴告警 | support.zte.com.cn 对应产品手册的"告警处理"分册 |
| 通用报错 | 把报错**原文加引号**搜 Google：`"error text here" cisco` |

## 3. 配置怎么写（场景配置）

| 场景 | 去哪 |
|---|---|
| 华为配置指南 | 华为支持站对应产品的《配置指南》分册（VLAN/OSPF/安全…） |
| Cisco 配置指南 | 产品页 → Configuration Guides |
| 配置样例 | GitHub 搜 `cisco {kw} config example`；Reddit r/networking 的 wiki |
| 中国社区样例 | 华为云社区 bbs.huaweicloud.com、知乎、51CTO（注意核对版本） |

## 4. 协议原理 / 标准

| 需求 | 去哪 |
|---|---|
| RFC 原文 | https://www.rfc-editor.org/（搜索框输协议名，如 OSPF → RFC 2328/5340） |
| 中文原理科普 | 知乎/信通院材料；NE StackExchange 的高票答案 |
| OID 含义（将来 SNMP 用） | https://oid-info.com/get/{oid}（如 1.3.6.1.2.1.1） |
| 端口/协议号 | https://www.iana.org/assignments/ |

## 5. 漏洞与安全公告

| 需求 | 去哪 |
|---|---|
| 华为安全公告 | https://www.huawei.com/cn/psirt |
| Cisco 安全公告 | https://www.cisco.com 寻找 PSIRT / Security Advisories 入口 |
| CVE 通用库 | https://nvd.nist.gov/vuln/search/results?query={kw}&search_type=all |
| 设备版本是否有已知漏洞 | NVD 搜"产品名+版本"，再看厂商 PSIRT 通告 |

## 6. 真机实测（免费环境）

| 需求 | 去哪 |
|---|---|
| 真实路由器只读命令 | RouteViews：`telnet route-views.routeviews.org`（用户 rviews 无密码，Cisco/Juniper 真机）；Web 版 https://www.routeviews.org/routeviews/ |
| 公共路由服务器目录 | https://www.routeservers.org/（77 台，telnet/SSH/平台清单） |
| HE 路由服务器 | `telnet route-server.he.net`（Cisco IOS CLI） |
| 可写实验沙箱 | Cisco DevNet Sandboxes：https://developer.cisco.com/sandbox/ （免费预约 IOS XE） |
| 自建仿真 | GNS3 / EVE-NG / 华为 eNSP（本地） |

## 7. 开发侧问题（hiq 自身）

| 需求 | 去哪 |
|---|---|
| Go 标准库/包 | https://pkg.go.dev/search?q={kw} |
| SSH 协议实现 | pkg.go.dev 搜 golang.org/x/crypto/ssh；其 GitHub issues |
| SNMP（将来） | github.com/gosnmp/gosnmp 的 README/Issues |
| Wails 框架 | https://wails.io/docs/ ；github.com/wailsapp/wails/discussions |
| 前端 | https://stackoverflow.com/search?q={kw} ；React/mermaid 官方文档 |
| 本仓库历史 | `git log --grep={kw}`；GitHub 界面搜 Issues/PR |

## 8. 求助社区（发帖前先搜）

| 社区 | 擅长 | 链接 |
|---|---|---|
| Network Engineering SE | 协议/设计问题，答案质量高 | https://networkengineering.stackexchange.com/search?q={kw} |
| Cisco Community | Cisco 具体产品 | https://community.cisco.com |
| 华为企业互动社区 | 华为产品，中文 | https://community.huawei.com/enterprise |
| Reddit r/networking | 经验/排错思路 | https://www.reddit.com/r/networking/ |

发帖模板：拓扑简图 + 设备型号/版本 + 配置片段（**去掉密码**）+ `show` 输出 + 已试过什么。

## 9. 搜索技巧

- 引号锁定报错原文：`"error text" huawei s5731`
- `site:` 限定站内：`site:support.huawei.com {kw}`、`site:networkengineering.stackexchange.com {kw}`
- 中英双查：中文社区偏操作，英文社区偏原理；同一问题两边都搜一遍
- 版本敏感：华为 VRP8/VRP5、Cisco IOS/IOS-XE 语法有差异，搜索词带上版本
- AI 时代仍要落到官方文档：模型给的命令先进 Info-Finder 验证，再进我们的读表
