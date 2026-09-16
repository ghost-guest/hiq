package tool

// PlanModeClassifier is an optional capability a Tool may implement to declare
// its stance on running during the planning phase. It is deliberately distinct
// from ReadOnly(): a tool can be a delegation that is safe only in a read-only
// variant (read_only_task). A false result is an explicit phase opt-out; tools
// without this interface continue to the ordinary Permissions/Sandbox path.
//
// Ported from DeepSeek-Reasonix internal/tool/tool.go.
type PlanModeClassifier interface {
	PlanModeSafe() bool
}
