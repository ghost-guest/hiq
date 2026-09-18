# NETDEV 设计稿导入与指纹发现 SPEC（Topo Design Import + Polite Fingerprint + Layered Discovery）

> **版本**: v1.4（全偏差闭环） | **日期**: 2026-08-30 | **状态**: T/F 全八批 v1 落地，§9.2 偏差全部闭环（v1.4）
>
> **来源**: 七轮调研与设计——①拓扑渲染与数据链路（TopologyMap/iptopo/topology.go）；②指纹发现
> 现状（discover/scanimport/snmp/locate）；③外部图工具生态（eNSP 已停更；drawio 明文 XML、
> vsdx 为 OOXML zip，均可解析）；④文件导入链路（Composer 三条附件通道 / ImportWizard 四段式 /
> netdev 持久化布局）；⑤多层发现设计（网段分级/预检-计划-确认流/指纹分层，来自弱口令双内网
> 场景推演）；⑥攻击路径推演（数据面替代横向登录的裁决）；⑦预算控制放开（上限拒绝改分段确认制）。
> v1.1 变更：新增 F4 多层发现与 F5 攻击路径推演两批次（§4.5/§4.6）、不变量 8、预检-计划-确认流、
> 预算语义放宽；两处一致性修正（vantage 表述、配置键去重）。
>
> **关联**: NETDEV_SPEC.md v2.1 §5（发现）、§八（按需停车场）；NETDEV_COMPLETION_SPEC.md §1（安全
> 不变量 1–4 沿用）；NETDEV_DASHBOARD_SPEC.md（总览页资产覆盖/在途区块消费本 spec 的
> DiscoveredHost 与 Job 数据）。
> 本 spec 不改动上述文档；立项后由人在 COMPLETION_SPEC 停车场表登记映射。

---

## §0 背景与问题定义

| 域 | 现状 | 问题 |
|---|---|---|
| 拓扑渲染 | 手写 SVG（NetDevLayout.tsx:2161-2280）：60×20 圆角矩形 + 9px 文字，无图标无设备类型 | 不可读；节点数据结构无 role 字段 |
| 拓扑数据 | 两个来源：plan（IP 规划本地推断，无边）/ snapshot（LLDP/CDP 实测，需手动校准） | 冷启动时空图；客户手里已有的 drawio/Visio 设计稿进不来 |
| 指纹发现 | TCP connect（默认仅 22/23，GUI 写死不可改）+ 96 字节被动裸 banner，不解析、不落库、前 8 台展示后即丢 | NETDEV_SPEC v2.1 §5.2 承诺的 sysDescr 指纹 / 待确认区 / 转正全部未落地；无任何指纹库 |
| 邻居信息 | CDP Platform 正则是死代码（topology.go:92 从未写入）；LLDP system description 直接丢弃 | 厂商/型号数据到手即扔 |

**用户决策已定**：eNSP(.topo) 因格式停更不做；做 drawio + Visio(.vsdx) 导入，主入口是"扔给对话框"；
指纹增强的唯一硬约束是**仅探测、绝不触发攻击类安全告警**（polite / quiet-by-default）。

---

## §1 范围

**做**（两域七批，T=topology / F=fingerprint；T2a/T2b 合称 T2 一个管线两期）：
- **T1 图标与 role 地基**——role 枚举 + 推断链 + 内联 SVG 图标集（无论是否导入都需要，是两域共享地基）
- **T2a 设计稿导入·drawio**——对话框/页签入口 → 四段式导入 → design 拓扑第三来源
- **T2b 设计稿导入·vsdx**——同管线，OOXML 解析器
- **F1 被动指纹第一批**——banner 结构化解析 + 发现结果落库（待确认区）+ 一键转正
- **F2 主动但礼貌的指纹**——SNMP sysDescr（可关）+ 邻居平台信息入库
- **F3 HTTP/TLS 应用指纹**（opt-in，默认关闭）
- **F4 多层发现**——递归资产测绘：表收集（被动）→ 缺口补测（礼貌）→ 转正扩点（人工），逐层深入
- **F5 攻击路径推演**——纯数据面图计算：暴露点 × 可达性 × 漏洞 → 路径图 + 暴露面报告（无任何探测/连接）

**不做**（停车场，维持或新增）：
- eNSP .topo 解析（格式停更，已裁决废弃）
- Visio 老二进制 .vsd（无文档，明确拒绝并给出文案）
- 设计稿双向同步/编辑（导入是一次性快照）
- netprobe 自动部署、IPv6、UDP 通用探测、内置调用 nmap（维持 NETDEV_SPEC v2.1 §八停车场）
- 拓扑人工覆盖层 GUI 编辑器（本 spec 的"设计稿坐标保留"是其导入版替代品）
- **使用弱口令评估所得凭证登录任何主机**——探测起点（vantage）凭证只允许来自密钥库的
  纳管凭证；"转正扩点"是人工配凭证，不是自动复用爆破结果

**安全不变量**（沿用 COMPLETION_SPEC §1 的 1–4，新增三条，每条方案须自检）：

5. **导入无副作用直达**：解析只产生预览（staged 文件 + 内存 diff）；任何清单/拓扑持久化必须经人工勾选确认（复用 ImportPreview 语义）。导入的凭证恒为空。
6. **指纹探测 quiet-by-default**：只读、单次、无探测 payload；限速 + 抖动 + 缓存 TTL；探测只从
   **纳管 vantage 的隧道**（F1-F3 为堡垒机；F4 起扩展到任一纳管设备，见不变量 8）或配置的
   scope 白名单内出发。§4.1 红线清单为禁入项，任何批次不得引入；fast_mode 等提速配置只调
   速率，不触碰红线。
7. **解析器永不 panic**：任意损坏/恶意构造的 .drawio/.vsdx 输入只允许返回错误或降级结果，不允许崩溃或越权读写（fuzz 验收）。
8. **vantage 凭证来源唯一**：多层发现（F4）的每个探测起点只允许使用密钥库中的**纳管凭证**；
   弱口令评估、banner、导入等任何途径获得的凭证/线索**永不**自动成为 vantage。层与层之间的
   扩展必须经"待确认区 → 人工转正 → 人工配凭证"，这是递归的天然人工门。

---

## §2 T1 图标与 role 地基

### 2.1 role 枚举（10 个，两域共用）

```
router | switch | firewall | ips | vpn | bastion | server | ap | cloud | unknown
```

- 层级仍由 tier 表达（0 核心/1 汇聚/2 接入/3 unmanaged），role 与 tier 正交：
  **tier=纵向位置，role=图标，健康=描边色（红/黄），managed=虚实线**。后三者现状已有，本批只补 role 维。
- 安全设备按用户要求显式区分：firewall / ips / vpn / bastion 四种；WAF、上网行为管理等后续用
  firewall + 徽章扩展，不在 v1 图标集内。

### 2.2 数据结构

- Go：`TopologyNode`（internal/netdev/topology.go）增加 `Role string` + `RoleSource string`
  （config|kind|group|model|vendor|neighbor|shape|label|position|none——kind/vendor 为落地时
  补充的两级：数据面类型与厂商默认）。
- 清单：`[netdev.devices]` 增加可选 `role` 字段（config/netdev.go NetDevDevice）——**用户显式指定恒最高优先级**，
  这就是"人工覆盖"的最小非 GUI 版。
- 前端：`NetDevTopologyNode`（types.ts:2349-2363）同步加字段；bridge mock 更新。

### 2.3 role 推断链（优先级从高到低）

| 序 | 来源 | 规则 |
|---|---|---|
| 1 | config 显式 role | 用户在清单里写死 |
| 2 | group 词匹配 | 扩展 inferTier 词表（iptopo.go:89-114 已 grep 核/汇/接/fw） |
| 3 | model/name 正则 | 词表见下 |
| 4 | 邻居平台信息 | CDP Platform / LLDP sysDescr（F2 落地后生效） |
| 5 | vendor 默认 | huawei-vrp/cisco-ios 无更多信息时 → switch（企业网多数） |
| 6 | unknown | 兜底，unmanaged 邻居默认 |

词表（中英双语，T1 与 T2 语义层共用同一份 Go 常量）：

| role | 关键词/型号前缀（样例） |
|---|---|
| firewall | fw, firewall, 防火墙, USG, USG6, ASA, FTD, 山石, AF- |
| router | router, 路由, RT-, AR\d+, NE\d+, ISR, ASR |
| switch | sw, switch, 交换, S\d{4}, CE\d{4}, Catalyst, Nexus, LS- |
| ips | ips, ids, 入侵检测, 入侵防御 |
| vpn | vpn, ipsec, ssl-网关, sslgw |
| bastion | bastion, 堡垒, jump |
| server | srv, server, esxi, pve, vm, node；清单 kind=docker/k8s 也归此（加徽章） |
| ap | ap\d*, ac\d*, 无线, wifi, wlan |
| cloud | internet, isp, 运营商, wan, cloud |

### 2.4 图标实现

- 新组件 `components/netdev/TopoIcon.tsx`：每 role 一个内联 SVG path（双色：前景色线条 + accent 高亮），
  经典符号语言（Cisco/eNSP 体系，运维零学习成本）：
  router=圆+四向箭头；switch=横矩形+交错四箭头；firewall=砖墙；ips=盾+眼；vpn=挂锁；
  bastion=终端；server=机架矩形；ap=圆点+波纹；cloud=云朵；unknown=虚线矩形+问号。
- 节点尺寸 60×20 → 约 76×30（图标 16-18px + 名称 10px），连线 opacity 0.4→0.65。
- 纯 SVG 路线，**不引第三方图库**；将来若上 React Flow 图标组件可直接复用。

### 2.5 验收

- mock 拓扑（bridge.ts:2516-2561 校园网样例）各节点出正确图标；tier/健康/managed 视觉不回归；
- config 显式 role 覆盖推断（单测）；词表命中表驱动单测（中英各抽 3 例/role）。

---

## §3 T2 设计稿导入（drawio / vsdx）

### 3.1 UX 流——四段式，复用 ImportWizard 已验证模式

```
选择/拖入 → Stage（Go 落盘换路径）→ Preview（解析+diff，无副作用）→ 人工勾选 → Apply（落库）
```

**入口 A（对话框，主入口）**：
1. 用户把 `.drawio/.vsdx` 拖进 Composer。三条既有通道全部天然支持：原生拖入
   `AttachDropped`（desktop/app.go:6266，工作区外文件自动拷入 `.hiq/attachments/`）、
   webview 内 drop（onFileDropCapture）、粘贴（SavePastedFile）——落点都是 Go 可读的稳定路径。
2. 消息附件 chips 渲染处（Message.tsx:78 parseAttachmentRefsForDisplay）按扩展名追加动作按钮
   **「解析为拓扑」**。
3. 点击 → `app.NetDevImportTopoPreview(path)` → 预览卡（见 3.3）。
4. **不依赖 agent/@引用注入**：二进制 @引用只会得到 "[binary file not shown]"（control/refs.go:442-443），
   解析全部在 Go 侧完成。agent 工具版（`netdev_import_topo`）留到 GUI 路径稳定后再注册。

**入口 B（拓扑页签，兜底）**：页签"导入设计稿"按钮 → `<input type=file accept=".drawio,.vsdx,.xml">`
→ FileReader → `app.NetDevStageBinary(name, dataURL)`（新增，参照 NetDevImportStageFile 的
text 版本补 bytes 版）→ 同一 Preview 流。

### 3.2 解析器分层（"怕做不好"的结构性解法：确定性分层 + 逐层降级）

**L0 容器层**
- drawio：`<mxfile><diagram>` XML；diagram 内容为 base64(deflate(xml)) 压缩变体时先解码；多页取
  节点数最多的一页，warning 提示存在其他页。
- vsdx：`archive/zip` → `visio/pages/page*.xml` + masters 索引（形状类型经 Master 名解析）。
  **.vsd 明确拒绝**，文案："请用 Visio 2013+ 另存为 .vsdx"。
- 护栏：文件 ≤5MB、解析后节点 ≤2000，超出直接拒绝并说明。

**L1 结构层（确定性，100% 可靠）**
- 产出：nodes{label, x, y, w, h, group} + edges{sourceKey, targetKey}。drawio 取 vertex=1 的 mxCell
  与 edge=1 的 source/target；vsdx 取 Shape 树（含嵌套展开）与 Connects。

**L2 语义层（启发式，带置信度）**
- role 来源优先级：**shape 库名 > 标签词表 > 几何位置兜底**。
  - shape 库名：drawio 用 Cisco/网络素材时 style 带 `shape=mxgraph.cisco.routers.*` /
    `mxgraph.networks.*`；vsdx 用 Master 名（Router/Switch/Firewall/路由器/交换机/防火墙…）。
  - 标签词表：§2.3 同一份 Go 常量。
  - 位置兜底：y 归一化分四档 → 仅作为 tier 参考，role 仍 unknown。
- 每节点输出 role + roleSource + confidence（shape=高，label=中，position=仅 tier）。

**L3 融合层**
- 与清单匹配：精确名 > 忽略大小写 > 节点 label 中提取的 IP == 设备地址 → managed 标记；
  输出三桶：已纳管 / 名称相似（人工裁决）/ 清单外新节点。

### 3.3 Preview 与 Apply

**Preview 返回**（无副作用，staged 路径服务端保持）：
```
ImportTopoPreview{
  Graph      // nodes(+role/roleSource/managed) + edges + source:"design"
  Stats      // 总数/已纳管/新增/未识别 role 数/页信息
  Warnings[] // 逐条："节点 X 未识别类型→unknown"/"多页已取最大页"/"压缩变体已解压"
}
```
前端预览卡：小图渲染（TopologyMap 直接吃 Graph）+ 统计 + warnings + 勾选（新节点逐个可排除）+ 两个选项。

**Apply（人工确认后，幂等覆盖）**：
1. 落 design 拓扑：`netdev/topology-design.json`（netdevStateDir 下新 state 文件，含
   imported_at / 源文件名 / graph）。审计记 `(import-topo)`。
2. 拓扑页签来源切换 chips 扩为 **plan | design | snapshot** 三选（现 plan/snapshot 基础上加一档）。
3. 布局模式（预览卡选项）：**默认 banded**（重新分带，与另两来源视觉一致）；
   可选 **original**（设计稿坐标按包围盒缩放映射）——这是"人工覆盖层"停车场项的导入版替代，
   设计者手工摆的布局经导入保留，不需要自研画布编辑器。
4. 可选"生成清单草稿"：勾选的新节点 → `applyConfigOnly` 写入 TOML 骨架（name + role + address?，
   凭证恒空）→ toast + 设备页签刷新。与 ImportWizard Apply 同构。

### 3.4 失败与降级

- 解析失败（加密/损坏/空图）→ 错误卡 + 一句建议（如"确认未加密、另存为标准 .drawio"）。
- 部分识别 → 永不整体失败，未识别节点 role=unknown 正常入图，warnings 列明。
- drawio 怪异保存（内嵌图片当节点等）→ 跳过 + warning。

### 3.5 测试策略（本批质量核心）

- **golden 样本**：`internal/netdev/testdata/topo/` 自造三档 drawio（带 Cisco 素材 / 纯矩形 / 多页压缩变体）
  + 两档 vsdx（标准 / 嵌套形状），表驱动断言 nodes/edges/role。
- **fuzz**：任意 bytes / 随机 XML / 截断 zip 输入 30s 不 panic 不越权（不变量 7）。
- 真机冒烟清单（移入验收文档）：客户真实设计稿 ≥2 份走全流程。

### 3.6 验收

- 带 Cisco 素材的 drawio：role 识别率人工抽检 ≥80%（shape 来源）；
- 纯矩形 drawio / vsdx：结构（节点+连线+层级）100% 导入，role=unknown；
- .vsd 拒绝文案出现；多页 warning 出现；
- 全流程（对话框拖入→预览→勾选→Apply→design 来源可见→可选转正）GUI 走通；
- 重复导入同一文件覆盖旧 design 快照，审计含两条 (import-topo)。

### 3.7 分期

- **T2a = drawio**（L0 XML 变体 + L1 + L2 + L3 全链，先行）。
- **T2b = vsdx**（仅 L0 换 zip/OOXML，L1-L3 复用；含 .vsd 拒绝）。

---

## §4 指纹发现增强（Polite Fingerprint）

### 4.1 探测宪法（写进 discover.go 顶部注释与本节，任何后续批次自检）

**三条默认**：
1. 只读、单次、无探测 payload——连上后要么等对端先说话（banner），要么发一次标准协议首包；
2. 慢、抖动、有缓存——per-host 串行 + 随机间隔 + TTL 内不重扫；
3. 永远从纳管 vantage 隧道/scope 白名单内出发（F4 前 vantage=堡垒机；F4 后为任一纳管设备，
   凭证来源见不变量 8），审计标记 (discover) 可自证。

**红线（禁入，永不实现）**：

| 红线 | 为什么 |
|---|---|
| 端口扫荡（单源短时大量端口/主机） | 命中 Snort/Suricata portscan 与防火墙 session 阈值 |
| SYN/FIN/NULL/Xmas 隐蔽扫描 | 经典 IDS 特征；我们只做全连接 connect |
| nmap -sV 式探测串 | 产品永不内置调用 nmap 的根本原因；nmap 导入保持用户自跑自责 |
| UDP 盲扫 | ICMP 不可达风暴必被记录 |
| SNMP community 爆破 | 触发设备 authFail trap；只用已知 community，失败不重试 |
| HTTP 路径枚举（/admin 等） | WAF 告警 |
| 畸形 TLS ClientHello（无 SNI/异常套件） | TLS 指纹检测告警 |

### 4.2 F1 被动解析 + 待确认区 + 转正（第一批，全部零新增流量）

1. **banner 结构化**（discover.go:133-144 现被动读处，上限 96→256 字节）：
   `parseBanner() → {kind: ssh|http|ftp|other, product, version, vendor_hint, raw}`；
   如 `SSH-2.0- Huawei-1.0` → product=Huawei, kind=ssh。纯字符串解析，单测表驱动。
2. **落库**：新 state `netdev/discovered/<ip>.json`（复用 finding 一文件一条模式，finding.go:57-133 同构）：
   ```
   DiscoveredHost{ ip, hostname?, vendor_hint?, role_hint?, first_seen, last_seen,
                   sources[], ports[{port, banner, parsed, at}] }
   ```
   合并策略按 ip upsert；sources ∈ {discover, nmap-import, topo-neighbor, locate-arp,
   layer-discover}。
   nmap 导入（scanimport.go）与拓扑邻居后续批次改写入此表而非各自为战。
3. **待确认区 GUI**：发现页签新增「待确认」区（ImportWizardCard 勾选卡样式复用）——替代现状
   "结果卡只显示前 8 台然后丢弃"（NetDevLayout.tsx:758-761）。全量主机、按 vendor_hint/端口聚合过滤。
4. **一键转正**：勾选 → 预填设备表单（name=hostname||ip，role=指纹推断，address=ip，
   vendor=推断驱动键||手选）→ 现有设备新增确认流（applyConfigOnly）。闭环"扫到→看到→纳管"。
5. **端口与入口**：默认端口集 22,23 → **22,23,161,443,830**（对齐 NETDEV_SPEC §5.1）；
   GUI 发现弹框允许自定义端口列表（修 netdev_app.go:1593 写死 nil）。

### 4.3 F2 SNMP sysDescr + 邻居平台（第二批）

1. **SNMP sysDescr**：新增 `[netdev.discovery] snmp_community`（默认空=**关闭**）。填开后，发现流对
   161 开放的待确认主机发**单次 GET** sysDescr/sysName（gosnmp 已在库，snmp.go 复用），失败不重试，
   结果写入 DiscoveredHost.vendor_hint/role_hint。不做 WALK、不试第二个 community。
2. **邻居平台激活**：topology.go:92 CDP Platform 死代码写入 DiscoveredHost；
   parseHuaweiLLDP 增取 system description。零新增探测流量（本就是密封只读命令的输出），
   同时作为 §2.3 role 推断链第 4 级输入——**拓扑图标与指纹在这里合流**。

### 4.4 F3 HTTP/TLS 应用指纹（第三批，opt-in）

- `[netdev.discovery] http_probe = false`（默认关）。开启后对 80/443/8080 开放主机：
  HTTP 单次标准 `GET /`（正常浏览器 UA）→ title/Server/X-Powered-By；
  TLS 标准握手（真实 SNI）→ 证书 CN/SAN/有效期。结果入 DiscoveredHost 新字段 `http{}`。
- 交付物定位：应用指纹（中间件/Web 服务识别），默认关闭即默认零新增流量。

### 4.5 F4 多层发现（递归资产测绘）

**动机**：堡垒机只看得到第一跳网段；完整资产地图需要逐层深入。多层 = 递归的
"读表（被动）→ 补测（礼貌）→ 转正扩点（人工）"，**每层的深入靠新纳管设备成为新 vantage，
不靠任何横向登录**（不变量 8）。

**三步循环**：

```
第 n 层 vantage（纳管设备，vault 凭证）
  ① 表收集（被动优先，零探测流量）
     交换机/路由器：ARP 表（locate.go 命令现成）、LLDP/CDP（topology.go 现成）、
                   路由表（新只读命令：display ip routing-table / show ip route）、
                   OSPF LSDB（可选：display ospf lsdb——一次读出全网拓扑，测绘金矿）
     主机：       本网段邻居（ip neigh / arp -a）
     产出：n+1 层候选资产（IP/MAC/主机名）+ 可达性边（arp-edge / route-edge）
  ② 缺口补测（主动，仅补空白）
     仅对没有任何表覆盖的直连网段，从最近 vantage 的 SSH 隧道做 polite TCP 扫
     （discover.go 的 dialerFor 从"堡垒机专用"通用化为"任意纳管 vantage"）
     scope 白名单逐跳校验：scope 外网段"记录不探测"（看到≠可连）
  ③ 转正扩点（人工门）
     新资产 → DiscoveredHost 待确认区 → 人工转正 + 人工配凭证 → 成为 n+1 层 vantage
```

**预检-计划-确认-执行流（L2"范围内批量"的范围确认落地形态）**：

多层发现不直接开扫。用户下发"查一层服务信息"后：

1. **预检（秒级，零探测流量）**：连接 vantage，读接口表 + 路由表 + ARP 表；
2. **网段分解**：路由表把聚合大块分解为**在用子网**——`10.0.0.0/8` 从不被整体扫描，
   它在路由表里实际是若干 /24；路由表未覆盖的空间 = 无在用证据 = 不探测（显示说明而非静默跳过）；
3. **计划卡（用户确认）**：

```text
发现计划（来源：vantage 主机 A 预检）
├ 直连 192.168.1.0/24 · 254 地址 · 预计 ~2 分钟 · [✓]
├ 路由 10.20.0.0/24  · 254 地址 · 预计 ~2 分钟 · [✓]
├ 路由 10.21.0.0/22  · 1024 地址 · 预计 ~8 分钟 · [ ]   ← 折叠默认不勾
├ ARP 已知存活 38 台（零探测成本，直接入账）· [✓]
└ 10.0.0.0/8 其余空间：路由表无在用子网证据 → 不探测
预算合计：主机 292+38 · 连接 ~1.2k · 墙钟 ~5 分钟
预设：[仅默认(推荐)] [全部小网段] [自定义]    指纹档位：T0+T1 (▾)
```

   三档预设：**仅默认**（直连小网段 + ARP 已知，推荐）/ **全部小网段**（≤/24 全勾）/
   **自定义**（逐网段勾选 + 调档位）；每行显示预计耗时，确认前看得见代价。
4. **执行 = Job 化**（防卡死，见下）。

**网段分级与默认动作**：

| 分级 | 定义 | 默认动作 |
|---|---|---|
| direct-small | 直连且 ≤/24 | 计划卡默认勾选 |
| routed-small | 路由可达且 ≤/24 | 计划卡默认勾选 |
| medium | /23–/21（≤2048） | 折叠显示，默认不勾，需确认（`medium_cidr_confirm` 可配置关闭） |
| large | >/21 或路由表未覆盖的聚合空间 | 默认路由表分解逐段进行；用户坚持整段时展示耗时数学（小时/天级）+ 显式确认后执行，**不硬性拒绝** |
| arp-known | ARP/邻居表中的存活主机 | 零成本直接入账，不需要任何探测 |

**指纹分层（默认便宜，逐级 opt-in）**：

| 档 | 成本 | 内容 | 默认 |
|---|---|---|---|
| T0 | 零 | ARP MAC → OUI 厂商 | 开 |
| T1 | 低 | 常用端口集 presence → **role 端口推测**（端口集映射表：3389→Windows 服务器、445→Windows、22→*nix/网络设备、23+161→网络设备、830→NETCONF 网络设备、6443→K8s…，与 §2.3 role 词表同源） | 开 |
| T2 | 中 | 开放端口 banner 被动读（单连接） | 仅小网段默认开；medium 需在计划卡升级 |
| T3 | 高 | SNMP sysDescr / HTTP / TLS | 恒 opt-in（F2/F3 门控） |

**防卡死与放开尺度（两层分离）**：

1. **工程卫生层（不可放开——这层不是"控制"，是 UI 不冻结的保证）**：
   发现任务编译为 Job（每网段一步）、进度走 live 事件流（前端永不等待完成）、结果落
   DiscoveredHost 存储 + 分页渲染、执行中随时暂停/取消。
2. **预算节奏层（放开——语义从"上限拒绝"改为"分段确认"）**：
   - **单步无固定地址上限**：步 = 网段本身（一个 /16 一步也行），计划卡按当前礼貌速率
     算出真实 ETA 展示——慢的根源是礼貌限速，不是地址数，不该用地址数设卡；
   - **预算耗尽 = 暂停不是失败**：墙钟/主机数到点时 Job 暂停在断点，弹"继续（追加预算）"
     一键接着跑，checkpoint 保留——预算是分段确认的节奏，不是天花板；
   - **默认值放宽**：单 Job 主机预算 65536（一个 /16）起步可配；发现类 Job 墙钟默认 4h
     （普通 Job 的 1800s 对发现场景太短）；medium 网段确认默认开、**可配置关闭**
     （授权场景的自担选择）；
   - **large 聚合块不硬拒**：默认仍走路由表分解（这是智能不是限制——分解后都是小段）；
     用户坚持对明确网段整段扫时，计划卡展示真实耗时数学（小时/天级），显式确认后照跑；
   - **礼貌限速默认不变**（rate 50/s、主机间 ~0.8s 抖动——这是 SOC 红线的事，不是卡死的
     事）；提供 `fast_mode` 配置（rate×4、delay→200ms）供授权窗口使用，开启时计划卡
     二次确认提示告警风险。

**递归控制（防失控，递归语义专属；其余配置键见 §4.7 统一表）**：

| 参数 | 默认 | 说明 |
|---|---|---|
| max_hops | 2（可配 1-4） | 递归深度上限；每层结束输出"下一层可见但未探索"清单供人工决策 |
| layer_host_budget | 512/层 | 每层候选资产处理上限 |
| per_vantage_rate | 继承全局 rate | 每个 vantage 的扫描速率不叠加特权 |
| vantage 并发 | ≤4 | 同层多个 vantage 并行读表，但主动补测全局串行限速 |

**数据产出**（喂 F5 与拓扑）：DiscoveredHost 新增来源 `layer-discover`；拓扑边新增
`arp-edge`/`route-edge` 类型（虚线弱边，与 LLDP 实边区分）；每跳审计
`(discover-layer, vantage, hop, budget_used)`。

**实现落点**：dialerFor 通用化（discover.go:160）；路由表/OSPF LSDB 解析器加入各厂商
只读命令表（driver/vendors.go，纯读无新风险）；循环控制器新文件 `layerdiscover.go`。

### 4.6 F5 攻击路径推演（纯数据面，零连接）

**定位**："如果这个暴露点失陷，波及多大"——回答这个问题的方式是图计算，不是实际走进去。
推演引擎**不发起任何网络连接**，输出一律标注"推演"（区别于实测）。

**输入**（全部已有/已 spec）：
- 暴露点：弱口令 Finding（assess）、基线 Finding（telnet-enabled、snmp-v1v2c 等，baseline.go）
- 可达性图：F4 的 arp/route 边 + LLDP/CDP 拓扑边 + via 链
- 资产与漏洞：DiscoveredHost 指纹、CVE 匹配、role（T1）

**计算**：从每个暴露点 BFS ≤3 跳；路径 = 暴露点 → 边 → 资产（指纹/CVE/角色）；
评分 = 路径上最大影响 × 最小修复成本（排优先级用）。

**输出**：
1. **路径图**——拓扑渲染复用 + 危险边着色（学 DRT fact-graph 的 exploits 红边）；
   每条路径可展开逐跳依据（哪张 ARP 表/哪条路由撑起这条边——分母可查）；
2. **暴露面报告**——暴露点 × 路径数 × 终点高危资产清单；
3. **修复优先级建议**——按"切断哪条边能消掉最多路径"排序，产物进提案草稿（人工审批后才存在执行）。

**验收**：构造已知 fixture 图 → 路径/评分断言；推演全程 tcpdump 零新增流量；
路径图每跳可深链到证据源。

### 4.7 节流与缓存（配置化，全部进 `[netdev.discovery]`）

| 键 | 默认 | 说明 |
|---|---|---|
| per_host_delay_ms | 800（±30% 抖动） | 主机间串行间隔（新增） |
| cache_ttl_hours | 24 | TTL 内已知主机不重扫（新增） |
| rate | 50（现状） | 全局并发上限不变 |
| ports | 22,23,161,443,830 | 默认端口集（GUI 可覆盖） |
| snmp_community | ""（关） | F2 |
| http_probe | false（关） | F3 |
| fingerprint_tier | 1 | F4 默认指纹档位（0-3），计划卡可临时上调 |
| medium_cidr_confirm | true | F4 medium 网段计划卡确认（可配置关闭） |
| max_hosts_per_job | 65536 | F4 单 Job 主机预算（默认一个 /16）；耗尽即暂停可追加 |
| discovery_wall_sec | 14400 | F4 发现类 Job 默认墙钟 4h（普通 Job 为 1800s） |
| fast_mode | false | F4 授权窗口提速档（rate×4、delay→200ms）；开启需计划卡二次确认（SOC 告警风险提示） |

### 4.8 SOC 共存 runbook

- 文档化：向 SOC 报备堡垒机 IP / 扫描窗口 / 端口范围；(discover) 审计标记用于事后自证。
- 建议话术模板进 docs，供交付实施人员使用。

### 4.9 验收

- F1：发现 100 台规模 → 待确认区全量可见、重启不丢（落库）；转正后设备进清单可纳管；
  parseBanner 表驱动单测覆盖 SSH/HTTP/乱码三态。
- F2：真机冒烟——配置 community 后 sysDescr 出现在待确认区；CDP Platform 出现在拓扑节点 title。
- F4：双网段环境（堡垒机只见 A，A 内纳管交换机可见 B）→ 第 1 层表收集出 B 段资产、
  转正一台 B 段主机后第 2 层解锁；max_hops 耗尽时输出"下一层可见未探索"清单；
  弱口令 Finding 的凭证**不会**出现在 vantage 候选里（不变量 8 回归测试）；
  scope 外网段只记录不探测。
- F4 交互流：预检→计划卡→确认→Job 执行全链走通——`10.0.0.0/8` 场景下计划卡展示的是
  路由表分解后的在用子网（永不出现"扫描 10/8"）；medium 网段默认不勾；三档预设切换正确；
  执行中可暂停/取消且 UI 不冻结（大结果集分页渲染）；指纹档位切换只影响后续主机。
- F4 分级单测：直连/路由/medium/large/arp-known 五分类的默认勾选与交互（large 默认分解、
  坚持整段时 ETA 数学展示 + 显式确认后执行；medium 可配置关闭确认）；
  **预算耗尽→暂停在断点→"继续"追加预算续扫**全链走通（不失败、不重复已完成网段）。
- F5：fixture 图路径/评分断言；推演全程 tcpdump 零新增流量；路径图逐跳可深链证据源；
  修复建议只生成提案草稿（无自动执行路径）。
- 全批：**tcpdump 对照抓包**——探测流量仅含 SYN/ACK/标准首包，无任何探测 payload（红线自检的客观证据）。
- 回归：scope 白名单拦截（不变量 4）与三条审计不变量不破。

### 4.10 前端与桥接（F 批次）

**UI 落点**：

| 件 | 位置 | 依据 |
|---|---|---|
| 待确认区列表（F1） | 发现页签新增分区，ImportWizardCard 勾选卡样式 | §4.2 已定 |
| 发现计划卡（F4） | 现有"发现"弹框位置升级（NetDevLayout C4.1 弹框 → 三态：CIDR 输入 / 预检结果+计划卡 / Job 进度） | 复用既有弹框入口，不新增页签 |
| 多层发现进度 | Job 进度卡（发现弹框第三态内嵌 + jobs 页签可追溯） | job.go 进度模型 |
| F5 路径图 | 拓扑页签来源过滤新增 `attackpath` 档（与 plan/design/snapshot 并列）+ 暴露面报告落 Finding 详情卡 | 拓扑 source 切换机制 |
| vantage 标记 | 设备页签设备卡角标（"vantage"chip，仅纳管且凭证在库的设备） | 转正扩点的可见性 |

**桥方法**（desktop 侧，全部走既有审计封装）：

```text
NetDevDiscoverPrecheck(vantage)        → PrecheckResult{interfaces, routes, arp_known, subnets[](分级)}
NetDevDiscoverPlan(vantage, overrides) → DiscoverPlan{steps[](每网段一步), budget, eta}
NetDevDiscoverConfirm(planId, chosen)  → JobId（编译为 Job，进既有进度/暂停/断点体系）
NetDevDiscoveredHosts(filter, page)    → 分页列表（待确认区数据源）
NetDevPromoteHosts(ip[], form[])       → 转正（复用 applyConfigOnly 确认流）
NetDevAttackPaths(scope)               → 路径图 + 暴露面报告（F5）
```

**审计事件**：`(discover-precheck)`、`(discover-layer, hop, vantage)`、`(discover-confirm,
plan_id, hosts, budget)`、`(promote, ips)`、`(attack-path, scope, path_count)`——全部进
既有哈希链。

---

## §5 与既有 spec 承诺的对照（旧债清算表）

| NETDEV_SPEC v2.1 §5 承诺 | 现状 | 本 spec 归属 |
|---|---|---|
| 隧道扫描 + banner 指纹 | banner 裸读不解析 | F1 ✅ |
| SNMP sysDescr 厂商指纹 | 未实现 | F2 ✅（礼貌约束版） |
| 待确认区 + 人工补凭证转正 | 未实现（结果即丢） | F1 ✅ |
| 端口 22/23/161/830 | 实际仅 22/23 且 GUI 写死 | 4.2.5 ✅ |
| CDP/LLDP 拓扑 | 已有但平台信息丢弃 | F2 ✅ |
| netprobe SFTP 自动部署 | 未做 | 维持停车场（不因本 spec 复活） |
| probe_fallback 配置消费 | 字段定义了零消费 | 维持停车场 |

---

## §6 分期与依赖

```
T1（地基，无依赖，先行）──┬→ T2a（drawio）→ T2b（vsdx）
                          └→ F1（落库+转正）→ F2（sysDescr+邻居）→ F3（opt-in HTTP/TLS）
                                        └→ F4（多层发现）→ F5（攻击路径推演）
```

| 期 | 内容 | 量级估计 | 前置 |
|---|---|---|---|
| T1 | role 字段+推断+图标 | 小 | 无 |
| T2a | drawio 全链 | 中 | T1（预览卡要渲染 role 图标） |
| T2b | vsdx | 小-中 | T2a（L1-L3 复用） |
| F1 | 解析+落库+待确认区+转正 | 中 | 无（与 T 并行） |
| F2 | sysDescr+邻居平台 | 小 | F1（要有落库点） |
| F3 | HTTP/TLS opt-in | 小 | F1 |
| F4 | 多层发现（dialerFor 通用化+表解析器+循环控制器） | 中 | F1（待确认区/转正是扩点闭环）；T1 的 role 增强推断 |
| F5 | 攻击路径推演引擎 | 中 | F4（可达性边）+ F1/F2（指纹与漏洞）+ T1（role） |

每期完成标准沿用仓库惯例：go 双模块 build/vet/test + 前端 tsc/vite/tsx 全绿，新增测试随期交付。

---

## §7 风险登记册

| 风险 | 概率/影响 | 缓解 |
|---|---|---|
| drawio 压缩变体/怪异保存解析失败 | 中/低 | L0 容错 + golden 三档样本 + 降级 warning 不失败 |
| vsdx 版本差异（2013/2016/2019+） | 中/中 | 样本驱动开发，不追求全格式；失败给明确文案 |
| role 语义识别率不达预期 | 中/中 | 保守 unknown + 用户清单显式 role 兜底（2.2） |
| SNMP 单次 GET 仍被严格 IPS 记录 | 低/中 | 默认关闭（空 community），开即知情 |
| 对话框入口与 @引用模型交互（防内容注入上下文） | 低/低 | 二进制引用维持 "[binary not shown]"，解析独立走 binding |
| 待确认区与 Finding 语义混淆 | 中/中 | 两者分离：Finding=问题证据，DiscoveredHost=资产线索；UI 分区明示 |
| F4 递归探测失控（层间放大） | 中/中 | max_hops/层预算/全局串行限速三层闸；每层结束出"下一层可见未探索"清单交人工决策 |
| F4 路由表/LSDB 解析各厂商差异 | 中/低 | 表驱动解析器 + 失败降级为"该 vantage 只贡献 ARP/LLDP 边" |
| F5 推演结果被误当实测 | 中/中 | 输出强制"推演"标注 + 零连接验收（tcpdump）+ 路径逐跳证据深链 |
| F4 的 vantage 被误配弱口令凭证 | 低/高 | 不变量 8：vantage 凭证来源唯一（密钥库纳管凭证）+ 回归测试 |

---

## §8 开放问题（待评审拍板）

1. **对话框入口触发方式**：附件 chips 上手动点「解析为拓扑」（本 spec 立场：可控、不打扰）vs
   netdev 模式下发送即自动解析（更魔法但可能误触发）。
2. **design 布局默认值**：本 spec 立场默认 banded 保一致、original 作选项——是否反过来
   （尊重设计稿布局为默认）？
3. **转正预填的 vendor 驱动键推断**：banner→驱动键映射（Huawei→huawei-vrp 等）v1 只做
   华为/思科两家 + 手选兜底，是否足够。
4. **待确认区数据保留策略**：是否需要自动清理（如 90 天未见即归档），v1 先不做是否可接受。
5. **F4 深度默认值**：max_hops=2 是否保守（本 spec 立场：2 起步，配合"下一层可见未探索"
   清单，让人工决定是否加深）。
6. **OSPF LSDB 是否进 F4 v1**：测绘价值最高但解析工作量最大（各厂商 LSDB 格式差异），
   本 spec 立场：v1 只做路由表，LSDB 留 v2。


---

## §9 落地记录（v1，2026-08-30）

### 9.1 已交付批次

| 批次 | 交付物 | 测试 |
|---|---|---|
| T1 | role 枚举/词表/推断链（role.go）+ TopoNode 字段 + TopoIcon 十图标 + 分带渲染 | 词表/优先级表驱动 |
| T2a | drawio 解析（topoimport.go，L0 压缩变体/多页）+ design 存储 + 预览卡/应用 | cisco/压缩/多页/健壮性 |
| T2b | vsdx 解析（topovsdx.go，OOXML zip/master 名/Connect 配对）+ .vsd 拒绝 | 内存构造包/健壮性 |
| F1 | parseBanner + DiscoveredHost 存储 + 待确认区 UI + 转正桥（凭证恒空） | banner/存储往返 |
| F2 | snmpFingerprint 单次 GET + 邻居平台入边/入库 + hints 不降级 | 平台断言/sysDescr 词表 |
| F3 | httpFingerprint 单请求（title/Server/证书）opt-in | httptest 真 HTTP/TLS |
| F4 | 预检三表（密封 Exec）+ 网段分级 + 计划卡确认 + vantage 隧道探测 + pacing 四键 | 解析/分级/计划/pacing |
| F5 | BuildAttackPaths 纯函数 + 切断建议 + 拓扑按钮/报告卡 + 早报接线 | 路径/评分/切断聚合 |

入口 A（聊天附件 chip「解析为拓扑」，仅 .drawio）经 `hiq:netdev-topo-import` 事件接线。

### 9.2 偏差（诚实记录；✅ = v1.3 收尾轮已闭环）

| 项 | 现状 | 计划 |
|---|---|---|
| 断点续扫 | ✅ 已闭环（原生实现，未动 job.go 密封引擎）：DiscoveryRunState 逐网段 checkpoint（discovery-run.json）+ 暂停桥（PauseDiscoverRun 按运行 ID 取消）+ 继续桥（DiscoverResume 从剩余段续扫，已完成段不重探；TTL 过滤使段内补测也便宜）+ 弹框「继续上次发现」横幅 | — |
| cache_ttl_hours | ✅ 已接：cacheTTLFilter 跳过 TTL 内已知主机（0→24h 默认，-1 关闭），hop 路径与 vantage 路径都过滤 | — |
| max_hops 跨层记账 | ✅ 已闭环：层账本（discovery-layers.json，设备→递归深度；转正时从线索继承层号）+ 线索盖层章（DiscoveredHost.Layer）+ layerGuard 硬守卫（默认 2，1-4 可配，超限拒绝并指名 max_hops 旋钮）| layer_host_budget/fingerprint_tier 两键仍为手动语义（计划卡已可视化），维持 |
| medium_cidr_confirm 配置键 | ✅ 已接（零值安全反转键 `medium_no_confirm`：默认 medium 不勾，显式信任才预勾） | — |
| pacing 四键 | ✅ fast_mode（rate×4 上限 256）/max_hosts_per_job（默认 /16，超限拒绝+指引）/discovery_wall_sec/per_host_delay_ms（默认 800ms±30% 抖动） | — |
| OSPF LSDB | 未做（§8 立场：v2） | 维持 |
| 入口 A 的 .vsdx | ✅ 已接：NetDevAttachmentBytesBase64（工作区路径门 + 8MB 上限），chip 同时支持 .drawio/.vsdx | — |
