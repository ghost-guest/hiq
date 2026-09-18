# PPT Skill

为 hiq 提供 PPT 生成能力。用户通过自然语言指令即可生成专业演示文稿。

## 功能

- **模板分析**：自动识别 PPT 模板结构，导出背景图
- **SVG 生成**：模型自由设计布局，生成高质量 SVG
- **质量检查**：自动检测文字溢出、内容密度、配色规范
- **视觉 QA**：用图片理解模型检查生成的 PPT 截图
- **PDF 导出**：支持 PPTX → PDF 转换
- **用户反馈**：支持"这页不好看"自动修正

## 安装

### 前置条件

- PowerPoint 或 WPS（用于模板分析）

### 安装

将 `skills/ppt/` 目录复制到 hiq 的 skill 目录。本 skill 需要 **系统 Python 3.10+**，并按 `requirements.txt` 安装依赖（`python3 -m pip install -r requirements.txt`）。大部分依赖为纯 Python，SVG→PPTX 主流程无需 Office/WPS。详细的 Python 环境准备见 `SKILL.md` 的"前置条件"一节。

```
hiq/
└── skills/
    └── ppt/                    (77MB, 零安装)
        ├── SKILL.md
        ├── template_config.json
        ├── README.md
        ├── templates/
        │   └── default.pptx
        ├── scripts/
        │   ├── analyze_template.py
        │   ├── fix_svg.py
        │   ├── check_svg.py
        │   ├── export_previews.py
        │   ├── export_pdf.py
        │   ├── save_notes.py
        │   ├── svg_to_pptx.py
        │   └── svg_to_pptx/
        └── python/             (内置 Python 运行时 + 依赖)
            └── runtime/
```

## 使用

在 hiq 中输入自然语言指令：

```
"用一个通用模板做一个华为智算中心的汇报PPT"
"帮我做一个关于产品规划的PPT，要有表格和甘特图"
"做一个10页的年终总结PPT，深色风格"
"第3页太空了，加点内容"
```

## 配置

编辑 `template_config.json` 可自定义设计规范：

- `template` — 默认模板路径
- `template_override` — 设置后覆盖默认模板
- `colors` — 配色方案
- `fonts` — 字体设置
- `rules` — 布局约束规则
- `mode` — 生成模式（fast/validate）

## 模式选择

| 模式 | 说明 | 适用场景 |
|------|------|----------|
| 快速模式 | 一次生成，不返工 | 快速出稿 |
| 校验模式 | 生成后检查，有问题返工 | 追求质量 |

## 文件说明

| 文件 | 说明 |
|------|------|
| SKILL.md | 模型读取的工作流指引 |
| template_config.json | 用户可编辑的设计约束 |
| templates/ | 模板文件目录 |
| scripts/analyze_template.py | 模板分析（导出背景图） |
| scripts/fix_svg.py | 修复 SVG XML 错误 |
| scripts/check_svg.py | 质量检查 |
| scripts/export_previews.py | 导出 PPT 截图 |
| scripts/export_pdf.py | PPTX → PDF 导出 |
| scripts/save_notes.py | 保存演讲者备注 |
| scripts/svg_to_pptx.py | SVG 转 PPTX |

## 依赖来源

- `svg_to_pptx` 模块来自 [ppt-master](https://github.com/anthropics/ppt-master) 项目
- 模板分析使用 Windows COM 自动化（PowerPoint/WPS）
- 内置 Python 3.12 + python-pptx + comtypes + lxml + PIL
