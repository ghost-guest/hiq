package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// hostFrame is one NDJSON line from the host's stdout: a response (id+result),
// an error response (id+error), or a notification (method).
type hostFrame struct {
	ID     json.Number     `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

// hostClient is a sequential JSON-RPC client over the host's stdio pipes: it
// writes one request, reads frames until the matching response arrives (any
// notifications emitted along the way are collected), then returns. This is the
// ordering a real desktop client uses.
type hostClient struct {
	t      *testing.T
	inW    *os.File
	outR   *os.File
	nextID int
	frames []hostFrame
	done   chan int
}

func startHostClient(t *testing.T) *hostClient {
	t.Helper()
	oldStdin, oldStdout := os.Stdin, os.Stdout
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin, os.Stdout = inR, outW
	c := &hostClient{t: t, inW: inW, outR: outR, done: make(chan int, 1)}
	go func() {
		rc := Run([]string{"host"}, "test-version")
		c.done <- rc
	}()
	t.Cleanup(func() {
		os.Stdin, os.Stdout = oldStdin, oldStdout
		_ = inR.Close()
		_ = outR.Close()
		_ = outW.Close()
	})
	return c
}

// call sends one request and blocks until its response frame arrives.
func (c *hostClient) call(method, paramsJSON string) hostFrame {
	c.t.Helper()
	c.nextID++
	id := c.nextID
	req := `{"jsonrpc":"2.0","id":` + itoa(id) + `,"method":` + jsonStr(method)
	if paramsJSON != "" {
		req += `,"params":` + paramsJSON
	}
	req += "}"
	if _, err := c.inW.WriteString(req + "\n"); err != nil {
		c.t.Fatal(err)
	}
	want := itoa(id)
	for {
		line, err := readHostLine(c.outR)
		if err != nil {
			c.t.Fatalf("reading %s response: %v", method, err)
		}
		var f hostFrame
		if json.Unmarshal([]byte(line), &f) != nil {
			c.t.Fatalf("unparseable frame %q", line)
		}
		c.frames = append(c.frames, f)
		if f.ID.String() == want {
			return f
		}
	}
}

// close ends the session; the host exits on stdin EOF.
func (c *hostClient) finish() {
	c.t.Helper()
	_ = c.inW.Close()
	rc := <-c.done
	if rc != 0 {
		c.t.Fatalf("host exit rc = %d", rc)
	}
}

func readHostLine(f *os.File) (string, error) {
	var buf []byte
	b := make([]byte, 1)
	for {
		n, err := f.Read(b)
		if n > 0 {
			if b[0] == '\n' {
				return string(buf), nil
			}
			buf = append(buf, b[0])
		}
		if err != nil {
			return string(buf), err
		}
	}
}

func itoa(n int) string { return json.Number(fmtInt(n)).String() }

func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func frameByID(t *testing.T, frames []hostFrame, id string) hostFrame {
	t.Helper()
	for _, f := range frames {
		if f.ID.String() == id {
			return f
		}
	}
	t.Fatalf("no response frame with id %s", id)
	return hostFrame{}
}

func notifications(frames []hostFrame, method string) []hostFrame {
	var out []hostFrame
	for _, f := range frames {
		if f.Method == method {
			out = append(out, f)
		}
	}
	return out
}

func hostTestProject(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "hiq.toml"), []byte(`
default_model = "local"

[[providers]]
name = "local"
kind = "acp-test-provider"
base_url = "http://example.invalid"
model = "fake-model"
api_key_env = "HIQ_TEST_KEY"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "hello.txt"), []byte("hi from host test"), 0o644); err != nil {
		t.Fatal(err)
	}
	return project
}

func TestHostProtocolPipe(t *testing.T) {
	isolateCLIConfigHome(t)
	t.Setenv("HIQ_TEST_KEY", "test-key")
	project := hostTestProject(t)
	c := startHostClient(t)
	defer c.finish()

	var hello struct {
		Version        string `json:"version"`
		HasModelConfig bool   `json:"hasModelConfig"`
	}
	if f := c.call("host/hello", ""); json.Unmarshal(f.Result, &hello) != nil || hello.Version != "test-version" || hello.HasModelConfig {
		t.Fatalf("host/hello = %+v", hello)
	}

	var cfgRes struct {
		Configured bool `json:"configured"`
	}
	if f := c.call("host/configure", `{"defaultModel":"local/fake-model","providers":[{"name":"pushed","kind":"acp-test-provider","apiKeyEnv":"HIQ_PUSHED_KEY","apiKey":"pushed-key","models":["pushed-model"]}]}`); json.Unmarshal(f.Result, &cfgRes) != nil || !cfgRes.Configured {
		t.Fatalf("host/configure = %+v err=%+v", cfgRes, f.Error)
	}

	newParams := `{"sessionId":"tab-1","cwd":` + jsonStr(project) + `}`
	var newRes struct {
		SessionID   string `json:"sessionId"`
		SessionPath string `json:"sessionPath"`
		Label       string `json:"label"`
	}
	if f := c.call("session/new", newParams); json.Unmarshal(f.Result, &newRes) != nil || newRes.SessionID != "tab-1" || newRes.SessionPath == "" || newRes.Label == "" {
		t.Fatalf("session/new = %+v err=%+v", newRes, f.Error)
	}

	var list struct {
		Entries []struct {
			Name string `json:"name"`
			Dir  bool   `json:"dir"`
		} `json:"entries"`
	}
	if f := c.call("fs/list", `{"sessionId":"tab-1"}`); json.Unmarshal(f.Result, &list) != nil {
		t.Fatalf("fs/list err=%+v", f.Error)
	}
	names := map[string]bool{}
	for _, e := range list.Entries {
		names[e.Name] = true
	}
	if !names["hello.txt"] || !names["hiq.toml"] {
		t.Fatalf("fs/list entries missing expected files: %+v", list.Entries)
	}

	var read struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	if f := c.call("fs/read", `{"sessionId":"tab-1","path":"hello.txt"}`); json.Unmarshal(f.Result, &read) != nil || read.Kind != "text" || read.Text != "hi from host test" {
		t.Fatalf("fs/read = %+v err=%+v", read, f.Error)
	}

	if f := c.call("fs/read", `{"sessionId":"tab-1","path":"../escape"}`); f.Error == nil || f.Error.Code != -32602 {
		t.Fatalf("fs/read escape guard = %+v, want invalid params error", f)
	}

	var gs struct {
		IsRepo bool   `json:"isRepo"`
		Root   string `json:"root"`
	}
	if f := c.call("git/status", `{"sessionId":"tab-1"}`); json.Unmarshal(f.Result, &gs) != nil || gs.IsRepo || gs.Root == "" {
		t.Fatalf("git/status = %+v err=%+v", gs, f.Error)
	}

	var state struct {
		Running       bool   `json:"running"`
		WorkspaceRoot string `json:"workspaceRoot"`
	}
	if f := c.call("session/state", `{"sessionId":"tab-1"}`); json.Unmarshal(f.Result, &state) != nil || state.Running || state.WorkspaceRoot == "" {
		t.Fatalf("session/state = %+v err=%+v", state, f.Error)
	}

	var sessions struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	if f := c.call("session/list", `{"cwd":`+jsonStr(project)+`}`); json.Unmarshal(f.Result, &sessions) != nil {
		t.Fatalf("session/list err=%+v", f.Error)
	}

	if f := c.call("session/close", `{"sessionId":"tab-1"}`); f.Error != nil {
		t.Fatalf("session/close error: %+v", f.Error)
	}
}

func TestHostSessionNewRejectsRelativeCwd(t *testing.T) {
	isolateCLIConfigHome(t)
	c := startHostClient(t)
	defer c.finish()
	if f := c.call("session/new", `{"sessionId":"t","cwd":"relative/path"}`); f.Error == nil {
		t.Fatal("session/new with relative cwd should fail")
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
