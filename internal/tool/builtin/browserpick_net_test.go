package builtin

import (
	"strings"
	"testing"

	cdpnetwork "github.com/chromedp/cdproto/network"
)

func TestPickStateLazyInitAndTake(t *testing.T) {
	s := &browserSession{id: "pick-test"}
	if s.pickState.Load() != nil {
		t.Fatal("pick state must start nil")
	}
	st := pickStateFor(s)
	if again := pickStateFor(s); again != st {
		t.Fatal("pickStateFor must return the same instance")
	}
	if d := st.result.Swap(nil); d != nil {
		t.Fatal("fresh state must have no result")
	}
	d := &PickDescriptor{Selector: "#main > button:nth-of-type(2)", Tag: "button", Text: "提交"}
	st.result.Store(d)
	if got := st.result.Swap(nil); got != d {
		t.Fatal("take must return the stored descriptor")
	}
	if again := st.result.Swap(nil); again != nil {
		t.Fatal("take must clear the result")
	}
}

func TestNetRecorderRingCap(t *testing.T) {
	s := &browserSession{id: "net-test"}
	st := netRecorderFor(s)
	if again := netRecorderFor(s); again != st {
		t.Fatal("netRecorderFor must return the same instance")
	}
	for i := 0; i < netCap+25; i++ {
		e := &netEntry{
			RequestID: cdpnetwork.RequestID(string(rune('a'+i%26)) + string(rune('a'+i/26))),
			Seq:       int64(i + 1),
			Method:    "GET",
			URL:       "https://example.test/api?i=X",
			Status:    200,
		}
		st.mu.Lock()
		st.byID[e.RequestID] = e
		st.entries = append(st.entries, e)
		if over := len(st.entries) - netCap; over > 0 {
			for _, old := range st.entries[:over] {
				delete(st.byID, old.RequestID)
			}
			st.entries = append(st.entries[:0:0], st.entries[over:]...)
		}
		st.mu.Unlock()
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.entries) != netCap {
		t.Fatalf("ring len = %d, want %d", len(st.entries), netCap)
	}
	if st.entries[0].Seq != 26 { // 325 inserted, ring holds the newest 300
		t.Fatalf("oldest surviving seq = %d, want 26", st.entries[0].Seq)
	}
}

func TestNetFormatEntry(t *testing.T) {
	e := &netEntry{Seq: 7, Method: "POST", URL: "https://api.test/v1/orders", Status: 500, Mime: "application/json", Type: "XHR"}
	got := netFormatEntry(e)
	for _, want := range []string{"#7", "POST", "500", "[XHR]", "https://api.test/v1/orders", "application/json"} {
		if !strings.Contains(got, want) {
			t.Errorf("entry %q missing %q", got, want)
		}
	}
	failed := &netEntry{Seq: 8, Method: "GET", URL: "https://api.test/x", Error: "net::ERR_NAME_NOT_RESOLVED"}
	if !strings.Contains(netFormatEntry(failed), "FAILED(net::ERR_NAME_NOT_RESOLVED)") {
		t.Errorf("failed entry format = %q", netFormatEntry(failed))
	}
}
