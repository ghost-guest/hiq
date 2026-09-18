package cli

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zzycxz/hiq/internal/config"
	"github.com/zzycxz/hiq/internal/netdev"
	"github.com/zzycxz/hiq/internal/trustdomain"

	"github.com/zzycxz/hiq/internal/trustdomain/nettrans"
)

// trustdomainCommand is the offline-capable surface of the trust domain
// (docs/TRUSTDOMAIN_SPEC.md §15). Subcommands:
//
//	hiq trustdomain init [--name X] [--data-dir D] [--force]
//	hiq trustdomain status [--data-dir D]
//	hiq trustdomain attest [--version V] [--policy H] [--data-dir D]
//	hiq trustdomain admit <key-file> [--name N] [--admin] [--data-dir D]
//	hiq trustdomain revoke <id-prefix> [--reason R] [--data-dir D]
//	hiq trustdomain token <subject-prefix> <resource> <ops> <ttl-sec> [--data-dir D]
//	hiq trustdomain run [--data-dir D] [--tick-sec N]
//
// Flags and positionals may appear in any order (tdExtractFlags). Power
// ops (admit/revoke) need admin quorum: they succeed offline on a
// single-admin bootstrap domain (quorum 1, what `init` creates); on
// multi-admin domains run them from a networked node once the Peer
// transport lands.
func trustdomainCommand(args []string, _ string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, trustdomainUsage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "init":
		return tdInit(rest)
	case "status":
		return tdStatus(rest)
	case "attest":
		return tdAttest(rest)
	case "admit":
		return tdAdmit(rest)
	case "revoke":
		return tdRevoke(rest)
	case "token":
		return tdToken(rest)
	case "identity":
		return tdIdentity(rest)
	case "join":
		return tdJoin(rest)
	case "exec":
		return tdExec(rest)
	case "sync":
		return tdSync(rest)
	case "anchor":
		return tdAnchor(rest)
	case "delegate":
		return tdDelegate(rest)
	case "succession":
		return tdSuccession(rest)
	case "promote":
		return tdPromote(rest)
	case "quorum":
		return tdQuorum(rest)
	case "pause":
		return tdPause(rest, false)
	case "resume":
		return tdPause(rest, true)
	case "run":
		return tdRun(rest)
	case "help", "--help", "-h":
		fmt.Print(trustdomainUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n%s", cmd, trustdomainUsage)
		return 2
	}
}

const trustdomainUsage = `用法: hiq trustdomain <子命令> [参数]（flag 与位置参数顺序任意）

  init    创建域：生成本机身份密钥 + 创世块（单管理员 quorum=1 引导域）
  status  读取账本：链头/成员/令牌/检查点/公告板
  identity 打印本机身份（ID + 公钥，供准入时交给管理员）
  join    加入已有域：<成员地址 host:port> <域ID>（域ID 带外获取；需已被准入）
  attest  发布本机自证摘要上公告板 [--version V] [--policy 策略哈希]
  admit   准入成员 <公钥文件> [--name N] [--admin]（需管理员 quorum）
  revoke  撤销成员 <ID前缀> [--reason R]（需管理员 quorum；见即生效）
  token   签发能力令牌 <成员ID前缀> <资源> <操作,操作> <有效期秒>
  exec    凭令牌发起委托工作：<成员地址> <令牌ID> <资源> <操作> <载荷JSON>
  sync    一次性对等同步：--bootstrap 拉取对端新块后退出（脚本友好）
  anchor  立即把本地 netdev 审计链头互锚上链（自动锚定外的手动触发）
  delegate 转授权令牌（深度限1）：<父令牌ID> <成员ID前缀> <资源> <操作,操作> <秒>
  succession 配置失联继任（quorum）：<小时> <成员ID前缀>...（管理员静默超时后自动晋补）
  promote  触发失联继任晋升（无需 quorum；dead-man 时钟到点才有效）
  quorum  提升管理员法定人数：<m>（引导域 M=1 → 准入更多管理员后升到 2/3，§6.2）
  pause   紧急刹车（quorum）：全网委托执行停止，本地诊断不受影响
  resume  解除紧急刹车（quorum）
  run     启动节点：--listen 监听 + --bootstrap 拨号 + --discover 局域网发现；--executor netdev 挂只读诊断执行器

通用 --data-dir 覆盖 [trustdomain].data_dir（默认 <用户配置根>/trustdomain）。
完整设计见 docs/TRUSTDOMAIN_SPEC.md。
`

// tdExtractFlags pulls "--name value" / "--name=value" pairs for the given
// flag names out of args, wherever they appear, and returns the remaining
// positional args in order. Go's flag package stops at the first positional,
// which would make "trustdomain token <id> <res> <ops> <ttl> --data-dir D"
// a usage error; this helper makes flag order irrelevant.
func tdExtractFlags(args []string, names ...string) (rest []string, vals map[string]string) {
	vals = map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		name := ""
		switch {
		case strings.HasPrefix(a, "--"):
			name = a[2:]
		case strings.HasPrefix(a, "-") && len(a) > 1:
			name = a[1:]
		default:
			rest = append(rest, a)
			continue
		}
		val, hasVal := "", false
		if eq := strings.Index(name, "="); eq >= 0 {
			val, name, hasVal = name[eq+1:], name[:eq], true
		}
		matched := false
		for _, n := range names {
			if name == n {
				matched = true
				break
			}
		}
		if !matched {
			rest = append(rest, a)
			continue
		}
		if !hasVal && i+1 < len(args) {
			i++
			val = args[i]
		}
		vals[name] = val
	}
	return rest, vals
}

// tdStr returns flags[name] or def when absent/empty.
func tdStr(flags map[string]string, name, def string) string {
	if v, ok := flags[name]; ok && v != "" {
		return v
	}
	return def
}

// tdHas reports whether a boolean flag was present.
func tdHas(flags map[string]string, name string) bool {
	_, ok := flags[name]
	return ok
}

// tdEnv resolves the data dir and identity for a command run.
type tdEnv struct {
	dir  string
	id   *trustdomain.Identity
	cfg  config.TrustDomainConfig
	full *config.Config
}

func tdPrepare(flagDir string) (*tdEnv, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("读取配置: %w", err)
	}
	td := cfg.TrustDomain
	dir := flagDir
	if dir == "" {
		dir = td.DataDirOrDefault()
	}
	if dir == "" {
		return nil, fmt.Errorf("无法解析数据目录：请在配置中设置 [trustdomain].data_dir")
	}
	id, err := trustdomain.LoadOrCreateIdentity(dir)
	if err != nil {
		return nil, fmt.Errorf("身份密钥: %w", err)
	}
	return &tdEnv{dir: dir, id: id, cfg: td, full: cfg}, nil
}

// tdChain loads the persisted ledger, requiring prior init.
func tdChain(env *tdEnv) (*trustdomain.Chain, error) {
	store, err := trustdomain.OpenStore(env.dir)
	if err != nil {
		return nil, err
	}
	c, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("读取账本（先 hiq trustdomain init）: %w", err)
	}
	return c, nil
}

// tdNode builds an offline node (no peers) over the loaded chain.
func tdNode(env *tdEnv, c *trustdomain.Chain) *trustdomain.Node {
	store, err := trustdomain.OpenStore(env.dir)
	if err != nil {
		store = nil
	}
	return trustdomain.NewNode(env.id, c, func() []trustdomain.Peer { return nil },
		trustdomain.NodeOptions{
			CheckpointEvery: env.cfg.CheckpointEveryBlocks,
			Store:           store,
		})
}

func nowSec() uint64 { return uint64(time.Now().Unix()) }

func tdInit(args []string) int {
	rest, flags := tdExtractFlags(args, "name", "data-dir", "force")
	if len(rest) > 0 {
		fmt.Fprintln(os.Stderr, "init 不接受位置参数")
		return 2
	}
	name := tdStr(flags, "name", "hiq-domain")
	force := tdHas(flags, "force")

	env, err := tdPrepare(flags["data-dir"])
	if err != nil {
		return tdErr(err)
	}
	if !force {
		if c, err := tdChain(env); err == nil {
			return tdErr(fmt.Errorf("已有域（%s，高度 %d）；--force 覆盖", trustdomain.DomainID(c), c.Height()))
		}
	}
	gen, err := trustdomain.BuildGenesis([]*trustdomain.Identity{env.id}, 1, name, nowSec())
	if err != nil {
		return tdErr(err)
	}
	c, err := trustdomain.ValidateChain([]*trustdomain.Block{gen})
	if err != nil {
		return tdErr(err)
	}
	store, err := trustdomain.OpenStore(env.dir)
	if err != nil {
		return tdErr(err)
	}
	if err := store.Save(c); err != nil {
		return tdErr(err)
	}
	fmt.Printf("域已创建\n  域 ID (genesis): %s\n  本机身份:  %s  (管理员)\n  账本:  %s\n", trustdomain.DomainID(c), env.id.ID(), store.Path())
	fmt.Println("\n引导域为单管理员 (quorum=1)。多管理员/多主机拓扑待 Peer 传输接入后经")
	fmt.Println("成员准入 + policy 记录扩展（docs/TRUSTDOMAIN_SPEC.md §五/§六）。")
	return 0
}

func tdStatus(args []string) int {
	_, flags := tdExtractFlags(args, "data-dir")
	env, err := tdPrepare(flags["data-dir"])
	if err != nil {
		return tdErr(err)
	}
	c, err := tdChain(env)
	if err != nil {
		return tdErr(err)
	}
	st := c.State()
	fmt.Printf("域 %s  高度 %d  链头 %s\n", trustdomain.DomainID(c), c.Height(), c.HeadHash().Hex())
	if st.Terminal {
		fmt.Printf("⚠ 域已于高度 %d 终止\n", st.TerminalHeight)
	}
	if h, ck, ok := c.LastCheckpoint(); ok {
		fmt.Printf("最近检查点: 高度 %d (%s)\n", h, ck.Hex())
	} else {
		fmt.Println("检查点: 无")
	}
	fmt.Printf("管理员 quorum: %d/%d\n", st.QuorumM, len(st.Admins()))
	fmt.Println("\n成员:")
	for _, m := range st.MemberIDs() {
		info := st.Member(m)
		role := "成员"
		if info.Admin {
			role = "管理员"
		}
		label := m
		if info.DisplayName != "" {
			label = fmt.Sprintf("%s (%s)", m, info.DisplayName)
		}
		fmt.Printf("  %s  %s  准入于高度 %d", label, role, info.AdmittedAt)
		if a := st.LatestAttestation(m); a != nil {
			fmt.Printf("  自证: v%s 策略%s", a.Version, shortHash(a.PolicyHash))
		}
		fmt.Println()
	}
	for _, id := range st.RevokedIDs() {
		fmt.Printf("  %s  [已撤销]\n", id)
	}
	return 0
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12] + "…"
	}
	return h
}

func tdAttest(args []string) int {
	rest, flags := tdExtractFlags(args, "version", "policy", "data-dir")
	if len(rest) > 0 {
		fmt.Fprintln(os.Stderr, "attest 不接受位置参数")
		return 2
	}
	env, err := tdPrepare(flags["data-dir"])
	if err != nil {
		return tdErr(err)
	}
	c, err := tdChain(env)
	if err != nil {
		return tdErr(err)
	}
	node := tdNode(env, c)
	if err := node.Attest(trustdomain.AttestationPayload{
		Version:    tdStr(flags, "version", "dev"),
		PolicyHash: flags["policy"],
	}, nowSec()); err != nil {
		return tdErr(err)
	}
	fmt.Printf("自证摘要已上链（高度 %d）\n", node.Chain().Height())
	return 0
}

func tdAdmit(args []string) int {
	rest, flags := tdExtractFlags(args, "name", "admin", "data-dir")
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "用法: hiq trustdomain admit <公钥文件> [--name N] [--admin]")
		return 2
	}
	raw, err := os.ReadFile(rest[0])
	if err != nil {
		return tdErr(err)
	}
	pub, err := decodePeerKey(strings.TrimSpace(string(raw)))
	if err != nil {
		return tdErr(err)
	}
	env, err := tdPrepare(flags["data-dir"])
	if err != nil {
		return tdErr(err)
	}
	c, err := tdChain(env)
	if err != nil {
		return tdErr(err)
	}
	node := tdNode(env, c)
	if err := node.ProposeQuorum(func(parent trustdomain.Hash) (*trustdomain.Record, error) {
		rec := trustdomain.NewMemberRecord(pub, flags["name"], tdHas(flags, "admin"), nowSec())
		if err := rec.SignAs(env.id, parent); err != nil {
			return nil, err
		}
		return rec, nil
	}); err != nil {
		return tdErr(fmt.Errorf("准入失败（多管理员域需联署）: %w", err))
	}
	fmt.Printf("成员 %s 已准入（高度 %d）\n", trustdomain.ID(pub), node.Chain().Height())
	return 0
}

func tdRevoke(args []string) int {
	rest, flags := tdExtractFlags(args, "reason", "data-dir")
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "用法: hiq trustdomain revoke <成员ID前缀> [--reason R]")
		return 2
	}
	env, err := tdPrepare(flags["data-dir"])
	if err != nil {
		return tdErr(err)
	}
	c, err := tdChain(env)
	if err != nil {
		return tdErr(err)
	}
	target, err := findMemberByPrefix(c, rest[0])
	if err != nil {
		return tdErr(err)
	}
	node := tdNode(env, c)
	if err := node.ProposeQuorum(func(parent trustdomain.Hash) (*trustdomain.Record, error) {
		rec := trustdomain.NewRevocationRecord(target, tdStr(flags, "reason", "revoked via CLI"), nowSec())
		if err := rec.SignAs(env.id, parent); err != nil {
			return nil, err
		}
		return rec, nil
	}); err != nil {
		return tdErr(fmt.Errorf("撤销失败（多管理员域需联署）: %w", err))
	}
	fmt.Printf("成员 %s 已撤销（高度 %d，见即生效）\n", target, node.Chain().Height())
	return 0
}

func tdToken(args []string) int {
	rest, flags := tdExtractFlags(args, "data-dir")
	if len(rest) != 4 {
		fmt.Fprintln(os.Stderr, "用法: hiq trustdomain token <成员ID前缀> <资源> <操作,操作> <有效期秒>")
		return 2
	}
	ttl, err := strconv.ParseUint(rest[3], 10, 64)
	if err != nil || ttl == 0 {
		return tdErr(fmt.Errorf("有效期必须是正整数秒"))
	}
	env, err := tdPrepare(flags["data-dir"])
	if err != nil {
		return tdErr(err)
	}
	c, err := tdChain(env)
	if err != nil {
		return tdErr(err)
	}
	subject, err := findMemberByPrefix(c, rest[0])
	if err != nil {
		return tdErr(err)
	}
	ops := strings.Split(rest[2], ",")
	for i := range ops {
		ops[i] = strings.TrimSpace(ops[i])
	}
	tokenID := "tok-" + hex.EncodeToString([]byte(fmt.Sprintf("%d-%s", nowSec(), subject)))[:12]
	node := tdNode(env, c)
	if err := node.IssueToken(trustdomain.TokenPayload{
		TokenID: tokenID, SubjectID: subject, Resource: rest[1],
		Operations: ops, ExpiresAt: nowSec() + ttl,
	}, nowSec()); err != nil {
		return tdErr(err)
	}
	fmt.Printf("令牌已签发\n  ID: %s\n  主体: %s\n  资源: %s  操作: %s\n  有效期至 unix %d\n",
		tokenID, subject, rest[1], strings.Join(ops, ","), nowSec()+ttl)
	return 0
}

// tdIdentity prints this host's domain identity (ID + public key). The key
// is what an admin admits: `trustdomain identity` on the new host, paste
// the public key into a file, `trustdomain admit <file>` on the admin side.
func tdIdentity(args []string) int {
	_, flags := tdExtractFlags(args, "data-dir")
	env, err := tdPrepare(flags["data-dir"])
	if err != nil {
		return tdErr(err)
	}
	fmt.Printf("本机身份\n  成员 ID: %s\n  公钥(base64): %s\n  密钥文件: %s\n",
		env.id.ID(), base64.StdEncoding.EncodeToString(env.id.Public), trustdomain.IdentityKeyPath(env.dir))
	return 0
}

// tdJoin bootstraps membership from a running member: the domain ID (the
// genesis hash) is learned out-of-band — the join-time trust anchor, same
// posture as linkpeer's QR fingerprint comparison (spec §6.1 准入).
func tdJoin(args []string) int {
	rest, flags := tdExtractFlags(args, "data-dir")
	if len(rest) != 2 {
		fmt.Fprintln(os.Stderr, "用法: hiq trustdomain join <成员地址 host:port> <域ID>")
		return 2
	}
	addr, domainID := rest[0], rest[1]
	env, err := tdPrepare(flags["data-dir"])
	if err != nil {
		return tdErr(err)
	}
	// Refuse to clobber an existing ledger silently.
	if _, err := tdChain(env); err == nil {
		return tdErr(fmt.Errorf("已有账本；如需重新加入请先清理数据目录"))
	}
	chain, err := nettrans.JoinChain(addr, env.id, domainID)
	if err != nil {
		return tdErr(fmt.Errorf("加入失败（确认：对端在线、本机已被准入、域ID 正确）: %v", err))
	}
	store, err := trustdomain.OpenStore(env.dir)
	if err != nil {
		return tdErr(err)
	}
	if err := store.Save(chain); err != nil {
		return tdErr(err)
	}
	st := chain.State()
	fmt.Printf("已加入域 %s（高度 %d，成员 %d 名，quorum %d/%d）\n",
		domainID, chain.Height(), len(st.MemberIDs()), st.QuorumM, len(st.Admins()))
	fmt.Printf("下一步: hiq trustdomain run --data-dir %s --bootstrap %s\n", env.dir, addr)
	return 0
}

func tdRun(args []string) int {
	rest, flags := tdExtractFlags(args, "data-dir", "tick-sec", "listen", "bootstrap", "executor")
	if len(rest) > 0 {
		fmt.Fprintln(os.Stderr, "run 不接受位置参数")
		return 2
	}
	env, err := tdPrepare(flags["data-dir"])
	if err != nil {
		return tdErr(err)
	}
	c, err := tdChain(env)
	if err != nil {
		return tdErr(err)
	}
	interval := env.cfg.TickIntervalOrDefault()
	if v := flags["tick-sec"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			interval = n
		}
	}
	listen := tdStr(flags, "listen", env.cfg.ListenAddrOrDefault())
	bootstraps := append([]string(nil), env.cfg.BootstrapPeers...)
	if v := flags["bootstrap"]; v != "" {
		bootstraps = append(bootstraps, strings.Split(v, ",")...)
	}
	useDiscover := tdHas(flags, "discover") || env.cfg.Discover

	ln, err := nettrans.Listen(listen)
	if err != nil {
		return tdErr(fmt.Errorf("监听 %s: %w", listen, err))
	}
	node := tdNode(env, c)

	// LAN discovery (spec §四①): signed beacons in, merged peer set out.
	var disc *nettrans.Discoverer
	ctxD, cancelD := context.WithCancel(context.Background())
	defer cancelD()
	if useDiscover {
		disc = nettrans.NewDiscoverer(3 * nettrans.DefaultBeaconInterval)
		// Our own announce address: listener host if concrete, else a
		// non-loopback interface hint.
		announceHost, _, _ := net.SplitHostPort(ln.Addr().String())
		if announceHost == "" || announceHost == "0.0.0.0" || announceHost == "::" {
			announceHost = nettrans.LocalAddrHint()
		}
		announceAddr := net.JoinHostPort(announceHost, fmt.Sprint(portOf(ln.Addr().String())))
		dport := env.cfg.DiscoveryPortOrDefault()
		if bconn, err := net.Dial("udp4", fmt.Sprintf("255.255.255.255:%d", dport)); err == nil {
			go func() {
				tick := time.NewTicker(nettrans.DefaultBeaconInterval)
				defer tick.Stop()
				defer func() { _ = bconn.Close() }()
				for {
					select {
					case <-ctxD.Done():
						return
					case <-tick.C:
						_ = nettrans.SendBeacon(bconn, nettrans.BuildBeacon(env.id, node, announceAddr, time.Now().UnixMilli()))
					}
				}
			}()
		}
		if lconn, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", dport)); err == nil {
			go func() { _ = nettrans.ListenBeacons(ctxD, lconn, disc, node) }()
		}
		fmt.Printf("局域网发现已启用（UDP 广播 :%d，宣告 %s）\n", dport, announceAddr)
	}

	node.SetPeers(func() []trustdomain.Peer {
		addrs := append([]string(nil), bootstraps...)
		if disc != nil {
			addrs = append(addrs, disc.Addresses(node.Identity())...)
		}
		peers := make([]trustdomain.Peer, 0, len(addrs))
		for _, addr := range addrs {
			addr = strings.TrimSpace(addr)
			if addr == "" {
				continue
			}
			peers = append(peers, nettrans.NewNetPeer(addr, env.id, nettrans.ChainLookup(node.Chain())))
		}
		return peers
	})
	// Executor: the only registered flavor today is netdev's read-only
	// diagnostics (health board / host triage) — spec §7.3 as-built.
	var handler nettrans.WorkHandler
	switch flags["executor"] {
	case "":
	case "netdev":
		netdev.InitAuditAnchoring(env.full) // daemon-side audits anchor too
		handler = netdev.NewManager(env.full).RemoteWorkHandler()
	default:
		return tdErr(fmt.Errorf("未知执行器 %q（可用: netdev）", flags["executor"]))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = nettrans.Serve(ctx, ln, env.id, node, handler) }()

	fmt.Printf("节点运行中：身份 %s，域 %s，高度 %d\n", env.id.ID(), trustdomain.DomainID(c), c.Height())
	fmt.Printf("监听 %s；拨号 %d 个对等节点；tick %ds（Ctrl-C 退出）\n",
		ln.Addr().String(), len(bootstraps), interval)
	if len(bootstraps) == 0 {
		fmt.Println("提示：--bootstrap host:port[,host:port] 指定对等节点（gossip 为拉取式，需双向配置）")
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			node.Tick()
			fmt.Printf("tick: 高度 %d 链头 %s\n", node.Chain().Height(), node.Chain().HeadHash().Hex())
		case <-sig:
			fmt.Println("\n节点已停止")
			return 0
		}
	}
}

// tdAnchor manually cross-anchors the local netdev audit chain head
// (spec §八) — normally automatic per threshold/cadence; this is the
// scripted/incident button.
func tdAnchor(args []string) int {
	_, flags := tdExtractFlags(args, "data-dir")
	env, err := tdPrepare(flags["data-dir"])
	if err != nil {
		return tdErr(err)
	}
	c, err := tdChain(env)
	if err != nil {
		return tdErr(err)
	}
	head := netdev.AuditChainHead()
	if head == "" {
		return tdErr(fmt.Errorf("本地审计链为空（尚无带哈希的审计条目）"))
	}
	node := tdNode(env, c)
	if err := node.AnchorAudit(head, nowSec()); err != nil {
		return tdErr(err)
	}
	fmt.Printf("审计链头已互锚（高度 %d）: %s…\n", node.Chain().Height(), head[:16])
	return 0
}

// tdSync performs one gossip round against --bootstrap peers and exits —
// the scripted-fleet companion to the long-running `run` (e.g. pull a
// freshly-issued token before an offline `exec`).
func tdSync(args []string) int {
	rest, flags := tdExtractFlags(args, "data-dir", "bootstrap")
	if len(rest) > 0 {
		fmt.Fprintln(os.Stderr, "sync 不接受位置参数")
		return 2
	}
	env, err := tdPrepare(flags["data-dir"])
	if err != nil {
		return tdErr(err)
	}
	c, err := tdChain(env)
	if err != nil {
		return tdErr(err)
	}
	addrs := append([]string(nil), env.cfg.BootstrapPeers...)
	if v := flags["bootstrap"]; v != "" {
		addrs = append(addrs, strings.Split(v, ",")...)
	}
	if len(addrs) == 0 {
		return tdErr(fmt.Errorf("没有对等节点：--bootstrap host:port[,host:port]"))
	}
	before := c.Height()
	node := tdNode(env, c)
	node.SetPeers(func() []trustdomain.Peer {
		peers := make([]trustdomain.Peer, 0, len(addrs))
		for _, a := range addrs {
			if a = strings.TrimSpace(a); a != "" {
				peers = append(peers, nettrans.NewNetPeer(a, env.id, nettrans.ChainLookup(node.Chain())))
			}
		}
		return peers
	})
	node.Tick()
	h := node.Chain().Height()
	if h == before {
		fmt.Printf("已同步：高度 %d（无新块）\n", h)
	} else {
		fmt.Printf("已同步：高度 %d → %d（+%d 块）\n", before, h, h-before)
	}
	return 0
}

// tdExec exercises a capability token against a remote member (spec §7.3
// requester side): builds and signs the delegation locally (fails fast on
// scope/expiry), sends it, prints the executor's output.
func tdExec(args []string) int {
	rest, flags := tdExtractFlags(args, "data-dir", "ttl")
	if len(rest) != 5 {
		fmt.Fprintln(os.Stderr, "用法: hiq trustdomain exec <成员地址> <令牌ID> <资源> <操作> <载荷JSON> [--ttl 秒]")
		return 2
	}
	addr, tokenID, resource, operation, payloadStr := rest[0], rest[1], rest[2], rest[3], rest[4]
	ttl := uint64(300)
	if v := flags["ttl"]; v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil && n > 0 {
			ttl = n
		}
	}
	if !json.Valid([]byte(payloadStr)) {
		return tdErr(fmt.Errorf("载荷必须是合法 JSON"))
	}
	env, err := tdPrepare(flags["data-dir"])
	if err != nil {
		return tdErr(err)
	}
	c, err := tdChain(env)
	if err != nil {
		return tdErr(err)
	}
	node := tdNode(env, c)
	peer := nettrans.NewNetPeer(addr, env.id, nettrans.ChainLookup(node.Chain()))
	out, err := node.RequestWork(peer, tokenID, resource, operation, []byte(payloadStr), ttl, nowSec())
	if err != nil {
		return tdErr(fmt.Errorf("委托失败（令牌范围/有效期/对端执行器）: %v", err))
	}
	fmt.Println(string(out))
	return 0
}

// tdQuorum raises the admin quorum threshold via a policy record — the
// second half of the founding flow: init (M=1) → admit --admin each peer
// → quorum 3 (spec §6.2 m-of-n). Lowering is refused: the ratchet only
// tightens (a compromised single admin must not be able to relax the
// brake).
func tdQuorum(args []string) int {
	rest, flags := tdExtractFlags(args, "data-dir")
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "用法: hiq trustdomain quorum <m>  （只能升高）")
		return 2
	}
	m, err := strconv.Atoi(rest[0])
	if err != nil || m < 1 {
		return tdErr(fmt.Errorf("m 必须是正整数"))
	}
	env, err := tdPrepare(flags["data-dir"])
	if err != nil {
		return tdErr(err)
	}
	c, err := tdChain(env)
	if err != nil {
		return tdErr(err)
	}
	st := c.State()
	if m < st.QuorumM {
		return tdErr(fmt.Errorf("法定人数只能升高（当前 %d，请求 %d）——放松门槛需人工重建域", st.QuorumM, m))
	}
	if m > len(st.Admins()) {
		return tdErr(fmt.Errorf("m=%d 超过现有管理员数 %d（先 admit --admin）", m, len(st.Admins())))
	}
	if m == st.QuorumM {
		fmt.Printf("法定人数已是 %d，无需变更\n", m)
		return 0
	}
	node := tdNode(env, c)
	if err := node.ProposeQuorum(func(parent trustdomain.Hash) (*trustdomain.Record, error) {
		rec := trustdomain.NewPolicyRecord(trustdomain.PolicyPayload{
			RuleVersion:      st.RuleVersion + 1,
			ActivationHeight: c.Height() + 1, // applies at its own block
			QuorumM:          m,
		}, nowSec())
		if err := rec.SignAs(env.id, parent); err != nil {
			return nil, err
		}
		return rec, nil
	}); err != nil {
		return tdErr(fmt.Errorf("变更失败（多管理员域需联署）: %w", err))
	}
	fmt.Printf("法定人数 %d → %d 已上链（高度 %d）——此后的权力变更需 %d 名管理员联署\n",
		st.QuorumM, m, node.Chain().Height(), m)
	return 0
}

// tdPause engages (resume=false) or lifts (resume=true) the quorum
// emergency brake (spec §6.4): all delegated work fleet-wide stops the
// moment the record is seen; local diagnostics are unaffected.
func tdPause(args []string, resume bool) int {
	rest, flags := tdExtractFlags(args, "data-dir", "reason")
	if len(rest) > 0 {
		fmt.Fprintln(os.Stderr, "pause/resume 不接受位置参数")
		return 2
	}
	env, err := tdPrepare(flags["data-dir"])
	if err != nil {
		return tdErr(err)
	}
	c, err := tdChain(env)
	if err != nil {
		return tdErr(err)
	}
	node := tdNode(env, c)
	reason := tdStr(flags, "reason", "paused via CLI")
	if err := node.ProposeQuorum(func(parent trustdomain.Hash) (*trustdomain.Record, error) {
		rec := trustdomain.NewPauseRecord(resume, reason, nowSec())
		if err := rec.SignAs(env.id, parent); err != nil {
			return nil, err
		}
		return rec, nil
	}); err != nil {
		return tdErr(fmt.Errorf("刹车失败（多管理员域需联署）: %w", err))
	}
	if resume {
		fmt.Printf("紧急刹车已解除（高度 %d）——委托执行恢复\n", node.Chain().Height())
	} else {
		fmt.Printf("紧急刹车已生效（高度 %d）——全网委托执行停止，本地诊断不受影响\n", node.Chain().Height())
	}
	return 0
}

// tdDelegate: a token holder derives a narrowed sub-token for another
// member (spec §13.2 #1) — subset scope, never outliving the parent.
func tdDelegate(args []string) int {
	rest, flags := tdExtractFlags(args, "data-dir")
	if len(rest) != 5 {
		fmt.Fprintln(os.Stderr, "用法: hiq trustdomain delegate <父令牌ID> <成员ID前缀> <资源> <操作,操作> <有效期秒>")
		return 2
	}
	ttl, err := strconv.ParseUint(rest[4], 10, 64)
	if err != nil || ttl == 0 {
		return tdErr(fmt.Errorf("有效期必须是正整数秒"))
	}
	env, err := tdPrepare(flags["data-dir"])
	if err != nil {
		return tdErr(err)
	}
	c, err := tdChain(env)
	if err != nil {
		return tdErr(err)
	}
	grantee, err := findMemberByPrefix(c, rest[1])
	if err != nil {
		return tdErr(err)
	}
	ops := strings.Split(rest[3], ",")
	for i := range ops {
		ops[i] = strings.TrimSpace(ops[i])
	}
	node := tdNode(env, c)
	if err := node.DelegateToken(rest[0], grantee, rest[2], ops, nowSec()+ttl, nowSec()); err != nil {
		return tdErr(err)
	}
	fmt.Printf("子令牌已转授给 %s（高度 %d，范围不得超出父令牌、寿命更短）\n", grantee, node.Chain().Height())
	return 0
}

// tdSuccession configures the dead-man policy (spec §13.2 #2).
func tdSuccession(args []string) int {
	rest, flags := tdExtractFlags(args, "data-dir")
	if len(rest) < 3 {
		fmt.Fprintln(os.Stderr, "用法: hiq trustdomain succession <小时> <成员ID前缀>...（至少一名继任者）")
		return 2
	}
	hours, err := strconv.ParseUint(rest[0], 10, 32)
	if err != nil || hours == 0 {
		return tdErr(fmt.Errorf("小时必须是正整数"))
	}
	env, err := tdPrepare(flags["data-dir"])
	if err != nil {
		return tdErr(err)
	}
	c, err := tdChain(env)
	if err != nil {
		return tdErr(err)
	}
	st := c.State()
	var successors []string
	for _, prefix := range rest[1:] {
		id, err := findMemberByPrefix(c, prefix)
		if err != nil {
			return tdErr(err)
		}
		successors = append(successors, id)
	}
	node := tdNode(env, c)
	if err := node.ProposeQuorum(func(parent trustdomain.Hash) (*trustdomain.Record, error) {
		rec := trustdomain.NewPolicyRecord(trustdomain.PolicyPayload{
			RuleVersion:        st.RuleVersion + 1,
			ActivationHeight:   c.Height() + 1,
			SuccessionAfterSec: uint32(hours) * 3600,
			SuccessionMembers:  successors,
		}, nowSec())
		if err := rec.SignAs(env.id, parent); err != nil {
			return nil, err
		}
		return rec, nil
	}); err != nil {
		return tdErr(fmt.Errorf("配置失败（需 quorum）: %w", err))
	}
	fmt.Printf("失联继任已配置：管理员静默 %d 小时后，%d 名继任者可晋升（trustdomain promote）\n", hours, len(successors))
	return 0
}

// tdPromote triggers the dead-man promotion (no quorum — the point).
func tdPromote(args []string) int {
	rest, flags := tdExtractFlags(args, "data-dir")
	if len(rest) > 1 {
		fmt.Fprintln(os.Stderr, "用法: hiq trustdomain promote [成员ID前缀]（默认本机）")
		return 2
	}
	env, err := tdPrepare(flags["data-dir"])
	if err != nil {
		return tdErr(err)
	}
	c, err := tdChain(env)
	if err != nil {
		return tdErr(err)
	}
	target := ""
	if len(rest) == 1 {
		if target, err = findMemberByPrefix(c, rest[0]); err != nil {
			return tdErr(err)
		}
	}
	node := tdNode(env, c)
	if err := node.PromoteSuccession(target, nowSec()); err != nil {
		return tdErr(fmt.Errorf("晋升失败（dead-man 时钟未到/非继任者）: %v", err))
	}
	fmt.Printf("继任晋升已上链（高度 %d）——晋升者即成为管理员\n", node.Chain().Height())
	return 0
}

// findMemberByPrefix resolves an unambiguous member ID prefix against the
// current state (active members first, then the revoked set).
func findMemberByPrefix(c *trustdomain.Chain, prefix string) (string, error) {
	st := c.State()
	var hits []string
	for _, id := range st.MemberIDs() {
		if strings.HasPrefix(id, prefix) {
			hits = append(hits, id)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	if len(hits) > 1 {
		return "", fmt.Errorf("前缀 %q 匹配多个成员，请加长", prefix)
	}
	for _, id := range st.RevokedIDs() {
		if strings.HasPrefix(id, prefix) {
			return "", fmt.Errorf("成员 %s 已是撤销状态", id)
		}
	}
	return "", fmt.Errorf("没有成员匹配前缀 %q", prefix)
}

// decodePeerKey accepts hex or std-base64 raw Ed25519 public keys.
func decodePeerKey(s string) ([]byte, error) {
	if raw, err := hex.DecodeString(s); err == nil && len(raw) == 32 {
		return raw, nil
	}
	if raw, err := base64.StdEncoding.DecodeString(s); err == nil && len(raw) == 32 {
		return raw, nil
	}
	return nil, fmt.Errorf("公钥必须是 32 字节 hex 或 base64")
}

// portOf extracts the port from host:port ("" on parse failure).
func portOf(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	return ""
}

func tdErr(err error) int {
	fmt.Fprintln(os.Stderr, "错误:", err)
	return 1
}
