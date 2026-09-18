# 办公自动化使用指南

## 概述

hiq 提供完整的办公自动化功能，包括 Word/Excel/PPT 文档处理、邮件管理、日历任务和定时任务，帮助您高效处理日常办公事务。

## 文档处理

### Word 文档

hiq 支持完整的 Word 文档操作：

- **创建文档**：使用 `doc_write` 工具
- **读取文档**：使用 `doc_read` 工具
- **插入图片**：在 sections 中添加 `{type: "image", image_path: "..."}`
- **生成目录**：在 sections 中添加 `{type: "toc", toc_level: 3}`

```json
{
  "path": "report.docx",
  "sections": [
    {"type": "heading", "text": "第一章", "level": 1},
    {"type": "image", "image_path": "/path/to/image.png", "image_alt": "Logo"},
    {"type": "toc", "toc_level": 3},
    {"type": "paragraph", "text": "正文内容"}
  ]
}
```

### Excel 表格

hiq 支持完整的 Excel 操作：

- **创建表格**：使用 `xlsx_write` 工具
- **读取数据**：使用 `xlsx_read` 工具
- **添加图表**：在 charts 数组中配置
- **条件格式**：在 cond_fmt 数组中配置

```json
{
  "path": "data.xlsx",
  "sheets": [{
    "name": "Sheet1",
    "cells": [
      {"ref": "A1", "value": "月份"},
      {"ref": "B1", "value": "销售额"}
    ],
    "cond_fmt": [{
      "range": "B2:B10",
      "type": "cell",
      "criteria": "greater_than",
      "value": "10000",
      "format": {"bg": "#00FF00"}
    }]
  }],
  "charts": [{
    "sheet": "Sheet1",
    "type": "bar",
    "title": "月度销售额",
    "data_range": "A1:B10",
    "position": "D2"
  }]
}
```

### PPT 演示

hiq 通过 PPT-auto Skill 提供专业的 PPT 生成能力：

- **SVG 自由设计**：完全自由的布局设计
- **模板填充**：基于现有模板填充内容
- **动画效果**：支持 fade、fly、zoom 等动画
- **过渡效果**：支持 fade、slide、zoom 等过渡

```bash
# 使用 PPT-auto Skill
hiq run "创建一个关于AI的PPT演示文稿"

# 带动画效果
hiq run "创建PPT，使用淡入动画"
```

## 邮件集成

### 配置邮箱

```toml
[cowork]
[[cowork.email_accounts]]
name = "工作邮箱"
smtp_host = "smtp.example.com"
smtp_port = 465
smtp_tls = true
imap_host = "imap.example.com"
imap_port = 993
username = "your@email.com"
password_env = "EMAIL_PASSWORD"
```

### 使用邮件功能

- **发送邮件**：使用 `email_send` 工具
- **读取邮件**：使用 `email_read` 工具
- **搜索邮件**：使用 `email_search` 工具

## 日历任务

### 创建定时任务

1. 打开「日历与任务」面板
2. 点击「新建任务」
3. 配置执行时间和提示词
4. 保存任务

### 任务类型

| 类型 | 说明 | 示例 |
|---|---|---|
| **定时提醒** | 在指定时间发送提醒 | 每天 9:00 提醒开晨会 |
| **定时执行** | 在指定时间执行任务 | 每周一生成周报 |
| **一次性任务** | 只执行一次 | 明天 15:00 发送邮件 |

### 配置

```toml
[cowork]
# 日历功能
calendar_enabled = true
```

## PPT 生成

### 使用 PPT 技能

1. 在对话中描述 PPT 需求
2. 选择 PPT 模板（可选）
3. AI 自动生成 PPT 文件

### PPT 模板

- **通用模板**：适用于大多数场景
- **自定义模板**：放置在 `.hiq/skills/ppt-auto/templates/`

### 配置

```toml
[cowork]
# PPT 模式
ppt_mode = "svg"  # svg 或 wps
ppt_active_template = ""  # 模板 ID
```

## 最佳实践

1. **邮箱配置**：使用授权码而非密码
2. **定时任务**：设置合理的执行时间，避免冲突
3. **PPT 生成**：提供详细的需求描述，效果更好
4. **资源管理**：定期清理不需要的定时任务和邮件
