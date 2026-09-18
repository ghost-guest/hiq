package rag

// templates.go manages Hyper-Extract template selection and recommendation.

import (
	"context"
	"strings"
	"time"
)

// TemplateInfo describes an extraction template.
type TemplateInfo struct {
	Name           string      `json:"name"`
	Category       string      `json:"category"`
	DisplayName    string      `json:"displayName"`
	Description    string      `json:"description"`
	Available      bool        `json:"available"`
	TemplateType   string      `json:"templateType"`
	EntityFields   []FieldMeta `json:"entityFields"`
	RelationFields []FieldMeta `json:"relationFields"`
	// NodePrompt is the stage-1 entity extraction prompt for this template.
	// Empty = use the default NodeExtractionPrompt from extract.go.
	NodePrompt string `json:"-"`
	// EdgePrompt is the stage-2 relation extraction prompt. Must contain exactly
	// two %s verbs: first for the known-nodes list, second for the chunk text.
	// Empty = use the default EdgeExtractionPrompt from extract.go.
	EdgePrompt string `json:"-"`
}

// FieldMeta describes a field in a template's entity or relation schema.
type FieldMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Built-in template definitions (always available even without Hyper-Extract).
// Only templates with REAL differentiated extraction logic are listed here.
// general/hypergraph, general/list, general/model were removed: they existed as
// placeholders mirroring Hyper-Extract's Python type system, but hiq's Go
// extractor has no hyperedge/list/model support (no custom prompts, fixed
// binary-graph result shape), so selecting them produced identical output to
// general/graph. The five domain templates below each carry distinct prompts.
var builtinTemplates = []TemplateInfo{
	{Name: "general/graph", Category: "general", DisplayName: "通用知识图谱", Description: "从任意文本中提取实体节点及二元关系", Available: true},
	{Name: "finance/graph", Category: "finance", DisplayName: "金融分析", Description: "金融领域知识图谱提取", Available: true, NodePrompt: financeNodePrompt, EdgePrompt: financeEdgePrompt},
	{Name: "medicine/graph", Category: "medicine", DisplayName: "医学知识", Description: "医学领域知识图谱提取", Available: true, NodePrompt: medicineNodePrompt, EdgePrompt: medicineEdgePrompt},
	{Name: "legal/graph", Category: "legal", DisplayName: "法律文书", Description: "法律领域知识图谱提取", Available: true, NodePrompt: legalNodePrompt, EdgePrompt: legalEdgePrompt},
	{Name: "industry/graph", Category: "industry", DisplayName: "工业知识", Description: "工业领域知识图谱提取", Available: true, NodePrompt: industryNodePrompt, EdgePrompt: industryEdgePrompt},
	{Name: "tcm/graph", Category: "tcm", DisplayName: "中医知识", Description: "中医领域知识图谱提取", Available: true, NodePrompt: tcmNodePrompt, EdgePrompt: tcmEdgePrompt},
}

// --- Domain-specific prompts ---

const financeNodePrompt = `你是金融知识抽取助手。从下面这段文本中抽取所有金融相关实体。

实体类型包括：公司/企业、基金/理财产品、行业/板块、财务指标、政策法规、金融产品、监管机构、市场事件、投资人物。
要求：
1. 实体 name 用规范化的简称（如"贵州茅台"而非"贵州茅台酒股份有限公司"），全文保持一致
2. 只抽取文本明确提到的实体，不要推理或脑补
3. 不要抽取纯代词或泛指，必须有独立指代意义
4. 描述简洁，控制在 50 字内，尽量包含关键财务数据

只返回 JSON：{"entities":[{"name":"...","type":"organization","description":"..."}]}
type 可选值：organization, product, concept, event, person, project, location, topic, other

### 文本：
%s`

const financeEdgePrompt = `你是金融关系抽取助手。下面已给出本段文本中抽取出的实体列表。请只在这些已知实体之间抽取关系。

关系类型侧重：投资/持股、竞争、供应链、合作、监管、隶属、并购、关联交易。
关键约束：
1. 关系的 source 和 target 必须出现在"已知实体"列表中
2. 只抽取文本明确提到的事实，不要推理
3. 描述简洁，控制在 50 字内

已知实体：
%s

只返回 JSON：{"relations":[{"source":"...","target":"...","type":"...","description":"..."}]}

### 文本：
%s`

const medicineNodePrompt = `你是医学知识抽取助手。从下面这段文本中抽取所有医学相关实体。

实体类型包括：疾病/诊断、症状/体征、药物/方剂、检查/检验、科室/专科、治疗方案/手术、患者群体、医疗设备、病原体。
要求：
1. 实体 name 用医学规范名称，全文保持一致
2. 只抽取文本明确提到的实体，不要推理
3. 描述简洁，控制在 50 字内，包含适应症/用法/禁忌等关键信息

只返回 JSON：{"entities":[{"name":"...","type":"concept","description":"..."}]}
type 可选值：concept, product, person, organization, event, project, location, topic, other

### 文本：
%s`

const medicineEdgePrompt = `你是医学关系抽取助手。下面已给出本段文本中抽取出的实体列表。请只在这些已知实体之间抽取关系。

关系类型侧重：治疗、禁忌、引起/导致、适应症、用法用量、检查项目、并发、鉴别诊断。
关键约束：
1. 关系的 source 和 target 必须出现在"已知实体"列表中
2. 只抽取文本明确提到的事实，不要推理
3. 描述简洁，控制在 50 字内

已知实体：
%s

只返回 JSON：{"relations":[{"source":"...","target":"...","type":"...","description":"..."}]}

### 文本：
%s`

const legalNodePrompt = `你是法律知识抽取助手。从下面这段文本中抽取所有法律相关实体。

实体类型包括：法律法规、案例/判例、当事人（原告/被告/上诉人）、法院/仲裁机构、罪名/案由、法律概念、合同/协议、律师/法官。
要求：
1. 实体 name 用规范名称，全文保持一致
2. 只抽取文本明确提到的实体，不要推理
3. 描述简洁，控制在 50 字内，包含法条编号/案号等关键标识

只返回 JSON：{"entities":[{"name":"...","type":"person","description":"..."}]}
type 可选值：person, organization, concept, event, project, product, location, topic, other

### 文本：
%s`

const legalEdgePrompt = `你是法律关系抽取助手。下面已给出本段文本中抽取出的实体列表。请只在这些已知实体之间抽取关系。

关系类型侧重：适用、违反、判决、起诉、代理、管辖、引用、上诉、调解。
关键约束：
1. 关系的 source 和 target 必须出现在"已知实体"列表中
2. 只抽取文本明确提到的事实，不要推理
3. 描述简洁，控制在 50 字内

已知实体：
%s

只返回 JSON：{"relations":[{"source":"...","target":"...","type":"...","description":"..."}]}

### 文本：
%s`

const industryNodePrompt = `你是工业知识抽取助手。从下面这段文本中抽取所有工业/制造相关实体。

实体类型包括：设备/机器、工艺/流程、产线/车间、标准/规范、故障/缺陷、参数/指标、原材料/零部件、产品/型号、维护方案。
要求：
1. 实体 name 用行业规范名称，全文保持一致
2. 只抽取文本明确提到的实体，不要推理
3. 描述简洁，控制在 50 字内，包含型号/参数/规格等关键技术信息

只返回 JSON：{"entities":[{"name":"...","type":"product","description":"..."}]}
type 可选值：product, concept, event, organization, person, project, location, topic, other

### 文本：
%s`

const industryEdgePrompt = `你是工业关系抽取助手。下面已给出本段文本中抽取出的实体列表。请只在这些已知实体之间抽取关系。

关系类型侧重：组成、工序、检测、维护、故障原因、参数范围、标准依据、替代。
关键约束：
1. 关系的 source 和 target 必须出现在"已知实体"列表中
2. 只抽取文本明确提到的事实，不要推理
3. 描述简洁，控制在 50 字内

已知实体：
%s

只返回 JSON：{"relations":[{"source":"...","target":"...","type":"...","description":"..."}]}

### 文本：
%s`

const tcmNodePrompt = `你是中医知识抽取助手。从下面这段文本中抽取所有中医相关实体。

实体类型包括：证型/病证、方剂/处方、中药/药材、穴位、治法、病因病机、经典著作、医家、症状/体征。
要求：
1. 实体 name 用中医规范名称，全文保持一致
2. 只抽取文本明确提到的实体，不要推理
3. 描述简洁，控制在 50 字内，包含性味归经/功效等关键信息

只返回 JSON：{"entities":[{"name":"...","type":"concept","description":"..."}]}
type 可选值：concept, product, person, organization, event, project, location, topic, other

### 文本：
%s`

const tcmEdgePrompt = `你是中医关系抽取助手。下面已给出本段文本中抽取出的实体列表。请只在这些已知实体之间抽取关系。

关系类型侧重：主治、组成、配伍、归经、功效、禁忌、出自、传承。
关键约束：
1. 关系的 source 和 target 必须出现在"已知实体"列表中
2. 只抽取文本明确提到的事实，不要推理
3. 描述简洁，控制在 50 字内

已知实体：
%s

只返回 JSON：{"relations":[{"source":"...","target":"...","type":"...","description":"..."}]}

### 文本：
%s`

// IsTemplate reports whether name is a known extraction template.
func IsTemplate(name string) bool {
	for _, t := range builtinTemplates {
		if t.Name == name {
			return true
		}
	}
	return false
}

// GetTemplatePrompt returns the domain-specific prompts for a template.
// Falls back to empty strings (caller should use default prompts) when the
// template is not found or has no custom prompts.
func GetTemplatePrompt(name string) (nodePrompt, edgePrompt string) {
	for _, t := range builtinTemplates {
		if t.Name == name {
			return t.NodePrompt, t.EdgePrompt
		}
	}
	return "", ""
}

var defaultEntityFields = []FieldMeta{
	{Name: "name", Description: "实体核心规范标示名称"},
	{Name: "type", Description: "实体的本体分类类别"},
	{Name: "description", Description: "基于上下文提炼的深度事实摘要"},
}

var defaultRelationFields = []FieldMeta{
	{Name: "source", Description: "关系起点源实体名称"},
	{Name: "target", Description: "关系指向目标实体名称"},
	{Name: "type", Description: "客观二元连线动词或关系"},
	{Name: "description", Description: "支撑该关联的上下文依据说明"},
}

// ListTemplates returns all available templates, ensuring built-in domain
// templates remain first-class citizens with rich schemas regardless of whether
// an external Hyper-Extract server is running.
func ListTemplates(heClient *HEClient) []TemplateInfo {
	// 1. Always build our rich built-in templates first.
	builtinMap := make(map[string]TemplateInfo, len(builtinTemplates))
	out := make([]TemplateInfo, 0, len(builtinTemplates))

	for _, bt := range builtinTemplates {
		ef := bt.EntityFields
		if len(ef) == 0 {
			ef = defaultEntityFields
		}
		rf := bt.RelationFields
		if len(rf) == 0 {
			rf = defaultRelationFields
		}
		ti := TemplateInfo{
			Name:           bt.Name,
			Category:       bt.Category,
			DisplayName:    bt.DisplayName,
			Description:    bt.Description,
			Available:      true, // Always guarantee built-in domain capabilities
			TemplateType:   bt.TemplateType,
			EntityFields:   ef,
			RelationFields: rf,
			NodePrompt:     bt.NodePrompt,
			EdgePrompt:     bt.EdgePrompt,
		}
		builtinMap[bt.Name] = ti
		out = append(out, ti)
	}

	// 2. If an external HE server is connected, merge in any extra/custom templates
	// without allowing it to disable or overwrite our rich built-in definitions.
	if heClient != nil {
		ctx, cancel := newCtx(5)
		defer cancel()
		if heTemplates, err := heClient.ListTemplates(ctx); err == nil {
			for _, t := range heTemplates {
				if _, exists := builtinMap[t.Name]; !exists && t.Available {
					ef := make([]FieldMeta, len(t.EntityFields))
					for i, f := range t.EntityFields {
						ef[i] = FieldMeta(f)
					}
					rf := make([]FieldMeta, len(t.RelationFields))
					for i, f := range t.RelationFields {
						rf[i] = FieldMeta(f)
					}
					out = append(out, TemplateInfo{
						Name:           t.Name,
						Category:       t.Category,
						DisplayName:    templateDisplayName(t.Name),
						Description:    t.Description,
						Available:      t.Available,
						TemplateType:   t.TemplateType,
						EntityFields:   ef,
						RelationFields: rf,
					})
				}
			}
		}
	}

	return out
}

// RecommendTemplate recommends a template based on document content analysis.
// Uses simple keyword matching for now.
func RecommendTemplate(content string) string {
	lower := strings.ToLower(content)

	// Finance keywords.
	financeWords := []string{"金融", "投资", "股票", "基金", "银行", "贷款", "利率", "finance", "investment", "stock", "bank"}
	for _, w := range financeWords {
		if strings.Contains(lower, w) {
			return "finance/graph"
		}
	}

	// Medical keywords.
	medWords := []string{"医学", "疾病", "症状", "治疗", "药物", "患者", "medical", "disease", "symptom", "treatment", "patient"}
	for _, w := range medWords {
		if strings.Contains(lower, w) {
			return "medicine/graph"
		}
	}

	// Legal keywords.
	legalWords := []string{"法律", "合同", "法规", "诉讼", "判决", "legal", "contract", "law", "court"}
	for _, w := range legalWords {
		if strings.Contains(lower, w) {
			return "legal/graph"
		}
	}

	// Industry keywords.
	indWords := []string{"工业", "制造", "生产", "工厂", "工艺", "industry", "manufacture", "production", "factory"}
	for _, w := range indWords {
		if strings.Contains(lower, w) {
			return "industry/graph"
		}
	}

	// TCM keywords.
	tcmWords := []string{"中医", "中药", "方剂", "针灸", "穴位", "经络", "辨证", "伤寒", "本草", "tcm", "acupuncture", "herbal"}
	for _, w := range tcmWords {
		if strings.Contains(lower, w) {
			return "tcm/graph"
		}
	}

	// Default: general graph.
	return "general/graph"
}

// templateDisplayName returns a human-readable name for a template.
func templateDisplayName(name string) string {
	parts := strings.SplitN(name, "/", 2)
	if len(parts) != 2 {
		return name
	}
	category, typ := parts[0], parts[1]
	categoryNames := map[string]string{
		"general":  "通用",
		"finance":  "金融",
		"medicine": "医学",
		"legal":    "法律",
		"industry": "工业",
		"tcm":      "中医",
	}
	typeNames := map[string]string{
		"graph":           "图谱",
		"hypergraph":      "超图",
		"list":            "列表",
		"model":           "模型",
		"set":             "集合",
		"spatial_graph":   "空间图",
		"temporal_graph":  "时序图",
		"biography_graph": "人物图",
		"concept_graph":   "概念图",
		"doc_structure":   "文档结构",
		"workflow_graph":  "工作流图",
	}
	catName := categoryNames[category]
	if catName == "" {
		catName = category
	}
	typeName := typeNames[typ]
	if typeName == "" {
		typeName = typ
	}
	return catName + typeName
}

// newCtx creates a context with timeout and returns it along with the cancel func.
// Callers MUST defer cancel() to avoid leaking the timer goroutine.
func newCtx(seconds int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
}
