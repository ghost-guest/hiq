package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGenSampleGongwenDoc generates a sample 公文 .docx to the user's Desktop
// for visual inspection. Skipped unless HIQ_GEN_SAMPLE=1 is set, so it
// never runs in normal CI/test runs. Run with:
//
//	HIQ_GEN_SAMPLE=1 go test ./internal/tool/builtin/ -run TestGenSampleGongwenDoc -v
func TestGenSampleGongwenDoc(t *testing.T) {
	if os.Getenv("HIQ_GEN_SAMPLE") != "1" {
		t.Skip("set HIQ_GEN_SAMPLE=1 to generate the sample doc")
	}
	home, _ := os.UserHomeDir()
	out := filepath.Join(home, "Desktop", "公文样例.docx")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}

	err := writeDOCX(DocInput{
		Path: out,
		Sections: []DocSection{
			// 大标题：2号宋体加粗居中
			{Type: "paragraph", Text: "关于推进九天大模型平台建设工作方案",
				Style: DocStyle{Font: "SimSun", Size: 44, Bold: true, Align: "center"}},
			// 正文开头：仿宋三号 首行缩进2字符
			{Type: "paragraph", Text: "为深入贯彻落实人工智能发展战略，加快推进中国移动九天（test-provider）大模型平台建设，提升企业级AI应用能力，现结合工作实际，制定本方案。",
				Style: DocStyle{Font: "FangSong", Size: 32, Indent: 2}},
			// 一级标题：黑体三号不加粗
			{Type: "heading", Level: 1, Text: "一、总体要求",
				Style: DocStyle{Font: "SimHei", Size: 32, Bold: false}},
			{Type: "paragraph", Text: "坚持以九天大模型为核心，构建覆盖编码、文档、数据分析的全场景智能助手体系，实现降本增效目标。",
				Style: DocStyle{Font: "FangSong", Size: 32, Indent: 2}},
			// 二级标题：楷体三号不加粗
			{Type: "heading", Level: 2, Text: "（一）指导思想",
				Style: DocStyle{Font: "KaiTi", Size: 32, Bold: false}},
			{Type: "paragraph", Text: "以自主研发与生态协同相结合，打造具有中国移动特色的AI编程智能体。",
				Style: DocStyle{Font: "FangSong", Size: 32, Indent: 2}},
			// 三级标题：仿宋三号不加粗
			{Type: "heading", Level: 3, Text: "1.建立协同研发机制",
				Style: DocStyle{Font: "FangSong", Size: 32, Bold: false}},
			// 四级标题：仿宋三号不加粗
			{Type: "heading", Level: 4, Text: "（1）每周召开技术例会",
				Style: DocStyle{Font: "FangSong", Size: 32, Bold: false}},
			// 一级标题 + 表格
			{Type: "heading", Level: 1, Text: "二、重点任务",
				Style: DocStyle{Font: "SimHei", Size: 32, Bold: false}},
			{Type: "paragraph", Text: "重点任务及分工如下表所示：",
				Style: DocStyle{Font: "FangSong", Size: 32, Indent: 2}},
			{Type: "table", Headers: []string{"任务项", "牵头部门", "配合部门", "完成时限", "备注"},
				Rows: [][]string{
					{"平台架构设计", "技术部", "架构组", "2026年8月底", "需专家评审"},
					{"核心模型适配", "算法部", "技术部", "2026年9月中旬", "对接300+模型"},
					{"桌面端开发", "前端组", "UI设计组", "2026年10月", "支持6平台"},
					{"安全合规审查", "安全部", "法务部", "2026年8月", "贯穿全周期"},
				},
				Style: DocStyle{HeaderBg: "#D9D9D9", Align: "center"}},
			// 一级标题 + 列表
			{Type: "heading", Level: 1, Text: "三、保障措施",
				Style: DocStyle{Font: "SimHei", Size: 32, Bold: false}},
			{Type: "list", Ordered: false, Items: []string{
				"加强组织领导，成立专项工作组",
				"保障资金投入，纳入年度预算",
				"强化人才支撑，开展专项培训",
			}},
			// 空行 + 附件
			{Type: "paragraph", Text: ""},
			{Type: "paragraph", Text: "附件：1.任务分解明细表",
				Style: DocStyle{Font: "FangSong", Size: 32, Indent: 2}},
			{Type: "paragraph", Text: "      2.进度横道图",
				Style: DocStyle{Font: "FangSong", Size: 32}},
		},
	})
	if err != nil {
		t.Fatalf("writeDOCX failed: %v", err)
	}
	t.Logf("sample doc written to %s", out)
}
