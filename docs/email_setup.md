# 邮件集成配置指南（多账号 + 周/月总结）

hiq 的邮件能力（SMTP 收发 + IMAP 读取/搜索 + 定时总结）已内置。本文档说明如何配置**多个邮箱账号**（如 139 个人邮箱 + 企业邮箱）、如何获取授权码、以及如何设置**每周/每月自动邮件总结**。

## 1. 配置多个邮箱

在 `config.toml`（或 coWork 设置面板）里用 `[[cowork.email_accounts]]` 数组配置。每个账号携带一个 `name`、`default` 标记、以及嵌套的 `[smtp]` 和 `[imap]`。工具通过 `account` 参数选择账号；留空 = 默认账号。

```toml
[[cowork.email_accounts]]
name    = "personal-139"     # 139 个人邮箱
default = true
[cowork.email_accounts.smtp]
host            = "smtp.139.com"
port            = 465
from            = "13800138000@139.com"
username        = "13800138000@139.com"
password_env    = "MAIL_PWD_PERSONAL"   # 只写变量名，授权码不进 TOML
encryption_mode = "tls"                 # 465→tls / 587→starttls / 25→none
[cowork.email_accounts.imap]
host         = "imap.139.com"
port         = 993
username     = "13800138000@139.com"
password_env = "MAIL_PWD_PERSONAL"

[[cowork.email_accounts]]
name    = "work-cmcc"        # 企业邮箱（示例，可替换为任意邮箱服务）
[cowork.email_accounts.smtp]
host            = "smtp.mail.139.com"
port            = 465
from            = "you@your-company.com"
username        = "you@your-company.com"
password_env    = "MAIL_PWD_WORK"
encryption_mode = "tls"
[cowork.email_accounts.imap]
host         = "imap.mail.139.com"
port         = 993
username     = "you@your-company.com"
password_env = "MAIL_PWD_WORK"
```

> **向后兼容**：旧的 `[cowork.smtp]` / `[cowork.imap]` 单账号写法仍然有效——加载时会自动折叠成 `email_accounts[0]`。无需改动旧配置。

## 2. 获取授权码（不是登录密码）

授权码在各邮箱网页版生成，**有效期约 90 天**，到期需重新生成：

- **139 邮箱**（个人 `@139.com`）：登录 mail.10086.cn → 设置 → 账号与安全 → 邮箱协议设置 → 开启 **IMAP/SMTP 服务** → 获取授权码。
- **企业邮箱**：联系单位邮箱管理员或在企业邮箱后台开启 IMAP/SMTP，按服务商提供的服务器地址配置（如 `imap.mail.139.com` / `smtp.mail.139.com`）。

拿到授权码后，在 **coWork 设置面板**的"邮件"区填入（密码框留空表示不修改；面板只显示"已设置/未设置"，授权码本身不会回传前端）。授权码会用 **Windows DPAPI** 加密存储在用户目录下，绑定当前 Windows 用户。

## 3. 每周 / 每月自动邮件总结

在 coWork 对话里让 agent 创建定时任务，或用 `schedule_create` 工具。关键点：
- `expression` 设定周期（每周五 18:00 = `0 18 * * 5`；每月 1 号 09:00 = `0 9 1 * *`）。
- `output_mode = "email"` + `output_dest = "你的邮箱;主题"`，总结会自动发到指定邮箱。
- `output_account` 选发件账号（留空 = 默认账号）。
- prompt 里用时间变量 `{week_start}` `{week_end}` `{last_month_start}` `{last_month_end}` 等，scheduler 在触发时自动替换成本地日期。

### 周总结（每周五 18:00）

```
schedule_create:
  name = "周邮件总结"
  expression = "0 18 * * 5"            # 每周五 18:00
  output_mode = "email"
  output_dest = "13800138000@139.com;本周邮件工作总结"
  output_account = "work-cmcc"         # 用政企账号发
  prompt = """
    请分别用 email_read 读取账号 personal-139 和 work-cmcc、
    时间范围 {week_start} 至 {week_end} 的收发邮件（account 参数指定账号，since/before 指定日期）。
    按【本周完成 / 下周计划 / 待跟进 / 外部沟通】分类总结，
    每项标注发件人与日期，末尾给出下周建议优先关注的 3 件事。
  """
```

### 月总结（每月 1 号 09:00）

```
schedule_create:
  name = "月邮件总结"
  expression = "0 9 1 * *"             # 每月 1 号 09:00
  output_mode = "email"
  output_dest = "13800138000@139.com;上月邮件工作总结"
  prompt = """
    请分别读取账号 personal-139 和 work-cmcc、
    时间范围 {last_month_start} 至 {last_month_end} 的收发邮件。
    按项目/主题归类，输出上月工作月报：主要成果、进展中的事项、需协调的问题。
  """
```

创建后可用 `schedule_run_now` 立即触发一次验证。app 内"自动化"页可回看历史总结。

## 4. 授权码过期处理

139 授权码约 90 天过期。hiq 会自动检测：
- 当 `email_read` / `email_send` 因授权码失效失败时，工具返回明确提示并弹**桌面 toast**。
- 对 `output_mode = "email"` 的定时任务，触发前会预检账号 IMAP 登录；失效则跳过本次（不浪费 token）并提示。
- 收到提示后，到 coWork 设置面板重新获取并填入新授权码即可。

## 时间变量速查

scheduler 在触发时把 prompt 里的这些占位符替换成本地日期：

| 占位符 | 含义 |
|---|---|
| `{today}` | 今天 |
| `{now}` | 现在（日期+时分） |
| `{week_start}` | 本周一 |
| `{week_end}` | 本周日 |
| `{month_start}` | 本月 1 号 |
| `{last_month_start}` | 上月 1 号 |
| `{last_month_end}` | 上月最后一天 |

未识别的占位符保持原样。周按周一到周日计算。
