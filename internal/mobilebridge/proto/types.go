// Package proto holds the wire-format message types shared between hiq
// desktop (S), linkpeer-signal cloud (K), and linkpeer mobile (C). It is the
// single source of truth for the JSON shapes on the wire; the Dart client
// mirrors these in lib/data/models/, and CI (protocol-compat) checks both
// sides stay in sync.
//
// Field names are camelCase JSON tags (a clean contract for non-Go clients).
// See docs/LINKPEER_PROTOCOL.md.
package proto

// Handshake version. Bumping this is a protocol break — clients must re-pair.
const Version = 1

// ClientHello is sent C→S on the DataChannel right after it opens (PROTOCOL §5.1).
// Signed with C's long-term Ed25519 key to authenticate identity; eph carries
// the X25519 ephemeral for forward-secret key agreement.
type ClientHello struct {
	T   string `json:"t"`   // "hello_c"
	Ver int    `json:"ver"` // Version
	Eph string `json:"eph"` // base64 X25519 ephemeral public key (32B)
	Nc  string `json:"nc"`  // base64 16B nonce (contributes to HKDF salt)
	Cid string `json:"cid"` // C deviceId
	Sid string `json:"sid"` // S deviceId
	Ts  int64  `json:"ts"`  // unix milliseconds
	Sig string `json:"sig"` // base64 Ed25519 signature over the hello fields
}

// ServerHello is the S→C reply (same shape, ns replaces nc).
type ServerHello struct {
	T   string `json:"t"`   // "hello_s"
	Ver int    `json:"ver"` // Version
	Eph string `json:"eph"` // base64 X25519 ephemeral public key (32B)
	Ns  string `json:"ns"`  // base64 16B nonce
	Cid string `json:"cid"`
	Sid string `json:"sid"`
	Ts  int64  `json:"ts"`
	Sig string `json:"sig"`
}

// Finished is the AEAD-encrypted confirmation (PROTOCOL §5.3). th binds the
// handshake transcript so neither side can be tricked into a downgraded one.
type Finished struct {
	T    string `json:"t"`    // "fin"
	Role string `json:"role"` // "c" or "s"
	Th   string `json:"th"`   // base64 transcript hash[:8]
}

// Command types (PROTOCOL §4.2). These travel as NDJSON inside AEAD frames
// after the handshake. The envelope's "t" selects the concrete command.
const (
	CmdSubmit        = "submit"
	CmdCancel        = "cancel"
	CmdSteer         = "steer"
	CmdPause         = "pause"
	CmdResume        = "resume"
	CmdApprove       = "approve"
	CmdAnswer        = "answer"
	CmdSetPlan       = "set_plan"
	CmdSubscribeTab  = "subscribe_tab"
	CmdListSessions  = "list_sessions"
	CmdLoadSession   = "load_session"
	CmdNewTab        = "new_tab"
	CmdSwitchTab     = "switch_tab"
	CmdSetModel      = "set_model"
	CmdOfficeRun     = "office_run"
	CmdFileStart     = "file_start"
	CmdFileChunk     = "file_chunk"
	CmdFileEnd       = "file_end"
	CmdResync        = "resync"
	CmdPing          = "ping"
	CmdPong          = "pong"
	CmdListModels    = "list_models"
	CmdListTemplates = "list_templates"
	CmdRenameSession = "rename_session"
	CmdDeleteSession = "delete_session"
)

// Envelope is the common command header. The router switches on T.
type Envelope struct {
	T   string `json:"t"`
	Tab string `json:"tab,omitempty"`
}

// Concrete commands. Each adds the fields its handler needs.
type SubmitCmd struct {
	T     string `json:"t"`
	Tab   string `json:"tab"`
	Input string `json:"input"`
	CmdID string `json:"cmd_id,omitempty"` // dedup id
}

type CancelCmd struct {
	T   string `json:"t"`
	Tab string `json:"tab"`
}

type SteerCmd struct {
	T    string `json:"t"`
	Tab  string `json:"tab"`
	Text string `json:"text"`
}

type ApproveCmd struct {
	T         string `json:"t"`
	Tab       string `json:"tab"`
	Approval  string `json:"approvalId"`
	Allow     bool   `json:"allow"`
	Session   bool   `json:"session,omitempty"`
	Persist   bool   `json:"persist,omitempty"`
}

type AnswerCmd struct {
	T       string     `json:"t"`
	Tab     string     `json:"tab"`
	Ask     string     `json:"askId"`
	Answers []string   `json:"answers"`
}

type SubscribeTabCmd struct {
	T   string `json:"t"`
	Tab string `json:"tab"`
}

type LoadSessionCmd struct {
	T    string `json:"t"`
	Path string `json:"path"`
}

type RenameSessionCmd struct {
	T     string `json:"t"`
	Tab   string `json:"tab"`
	Title string `json:"title"`
}

type DeleteSessionCmd struct {
	T   string `json:"t"`
	Tab string `json:"tab"`
}

type OfficeRunCmd struct {
	T        string            `json:"t"`
	Tab      string            `json:"tab"`
	Template string            `json:"template"`
	Args     map[string]string `json:"args,omitempty"`
}

type FileStartCmd struct {
	T      string `json:"t"`
	Tab    string `json:"tab"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Sha256 string `json:"sha256,omitempty"`
}

type FileChunkCmd struct {
	T    string `json:"t"`
	Tab  string `json:"tab"`
	Seq  int    `json:"seq"`
	Data string `json:"data"` // base64 分片
}

type FileEndCmd struct {
	T    string `json:"t"`
	Tab  string `json:"tab"`
	Name string `json:"name"`
}

type NewTabCmd struct {
	T             string `json:"t"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	Profile       string `json:"profile,omitempty"`
	TopicID       string `json:"topicId,omitempty"`
}

type SetModelCmd struct {
	T     string `json:"t"`
	Tab   string `json:"tab"`
	Model string `json:"model"`
}

type ResyncCmd struct {
	T        string `json:"t"`
	Tab      string `json:"tab"`
	SinceSeq uint64 `json:"since_seq"`
}

type PingCmd struct {
	T   string `json:"t"`
	Ts  int64  `json:"ts"`
}

// ListSessions/NewTab/SwitchTab/Pause/Resume/SetPlan/OfficeRun reuse Envelope
// or carry their own fields; handlers parse what they need.
