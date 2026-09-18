# hiq-server（linkpeer-signal 云端信令 K）

linkpeer × hiq 的**无状态信令路由器**：配对撮合、公钥交换、SDP/ICE 中转。
不碰任何业务数据/私钥，重启即清空，靠客户端持钥自洽认证。

## 架构（docker-compose 三件套）

```
公网 443 (WSS) ──→ Caddy (自动 TLS + /metrics 拦截) ──→ signal:8080 (Go HTTP)
                         │
                         └── coturn:3478/udp+tcp (STUN 打洞) + 49160-65535/udp (TURN relay)
```

- **signal**：纯 Go 静态二进制（distroless nonroot），5 个端点
  `/pair/register` `/pair/exchange` `/pair/confirm` `/session/ws` `/healthz` + `/metrics`
- **caddy**：自动 Let's Encrypt TLS + WSS 反代 + JSON 访问日志（100mb×7 滚动）+ `/metrics` 公网拦截
- **coturn**：STUN 打洞 + opt-in TURN 中转兜底（`use-auth-secret` REST 凭据）。
  TURN 只转发 DTLS 加密包——**中继无法解密业务流量**；ICE 优先直连候选，
  relay 仅在打洞全败（双对称 NAT）时启用。要回到纯 STUN：turnserver.conf
  加回 `no-udp-relay` + `no-tcp-relay` 并关掉 relay 端口段。

## 独立编译（不依赖 hiq 主体）

```bash
# 在 hiq 仓库根目录
CGO_ENABLED=0 go build -o linkpeer-signal ./cmd/linkpeer-signal
./linkpeer-signal -config deploy/linkpeer-signal/signal.toml
```

产物是单文件静态二进制，可直接丢 VPS 裸跑（明文 HTTP，需自带反代上 TLS）。

## 云服务器部署（docker-compose，推荐）

### 1. VPS 准备
- 4C8G 起步（5000 并发 WS），Ubuntu 22.04+
- 装 Docker + docker-compose
- 主机层提 fd 上限：`/etc/security/limits.conf` 加 `* soft nofile 65535` + `* hard nofile 65535`
- 防火墙（ufw）：
  ```
  22/tcp      # SSH（建议密钥 + 限源 IP）
  80/tcp      # ACME 挑战
  443/tcp     # WSS
  3478/udp    # STUN / TURN
  3478/tcp    # TURN over TCP（封 UDP 网络的兜底）
  49160:65535/udp  # TURN relay 端口段（turnserver.conf min/max-port）
  ```

### 2. 域名
A 记录 `signal.yourdomain.com` → VPS IP（TTL 60s，便于灾备切换）。

### 3. 改配置
```bash
cd deploy/linkpeer-signal
# Caddyfile：signal.example.com → signal.yourdomain.com
# signal.toml：[server] public_relay = "wss://signal.yourdomain.com"
#              [stun]  servers = ["stun:signal.yourdomain.com:3478"]
```

### 4. 启动
```bash
docker compose up -d --build
# Caddy 首次启动自动申请 Let's Encrypt 证书
```

### 5. 验证
```bash
curl https://signal.yourdomain.com/healthz
# {"ok":true,"online":0,"uptime":...,"goroutines":...,"heap_mb":...}

# /metrics 公网应 403
curl -o /dev/null -w "%{http_code}\n" https://signal.yourdomain.com/metrics
# 403

# STUN 反射测试（装 stunclient）
stunclient signal.yourdomain.com
```

## 安全清单

| 项 | 状态 |
|----|------|
| ECDH + AEAD AES-256-GCM + 分方向密钥 | ✅ |
| 无状态 WS 认证（Ed25519 + devId 自洽 + ts 窗口）| ✅ |
| 配对码爆破防护（TTL 60s + 5 次锁 + 限流 + 29bit 熵）| ✅ |
| 全局内存上限（pairs 50000 + peers 50000）| ✅ |
| per-dev WS 消息限流 50/s | ✅ |
| 帧解密失败 10/min 断连 | ✅ |
| ClientHello 版本校验（防降级）| ✅ |
| server_shutdown WS 广播（优雅重连）| ✅ |
| 命令级权限校验 + 设备吊销 | ✅ |
| 脱敏审计日志（devId 截断，不记业务）| ✅ |
| `/metrics` 公网拦截 | ✅（Caddyfile）|
| Cloudflare 前置（DDoS + WS）| ⚠️ 需手动接橙云 |
| rekey（2^32 主动轮换）| ⚠️ 当前 fail-close 兜底，待补 |

## 运维

```bash
# 日志（JSON 访问日志在 /data/access.log，应用 stdout 走 docker logs）
docker compose logs -f signal

# 监控（/metrics 内网抓取，Prometheus 格式）
# 在内网机器：curl http://<vps内网IP>:8080/metrics

# 重启（改配置后）
docker compose restart signal

# 升级
git pull && docker compose up -d --build
```

## 容量

单实例 4C8G 支撑 ~5000 并发 WS（ENGINEERING §10.6）。更高用 DNS 轮询 + 多实例
（K 无状态，任意实例都能服务任意设备）。

## 相关文档
- 协议规格：`docs/LINKPEER_PROTOCOL.md`
- K 规格：`docs/LINKPEER_SIGNAL_SPEC.md`
- 验证：`docs/LINKPEER_VERIFICATION.md`
