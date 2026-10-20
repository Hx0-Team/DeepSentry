package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type queryFixtureBackend struct {
	*fakeBackend
	search     ElementQueryResult
	queryError error
	queryHook  func()
	request    Request
}

func (b *queryFixtureBackend) Query(_ context.Context, req Request) (ElementQueryResult, error) {
	b.request = req
	if b.queryHook != nil {
		b.queryHook()
	}
	return b.search, b.queryError
}
func queryFixture(t *testing.T) (*Service, *queryFixtureBackend) {
	t.Helper()
	_, base := fixture(t)
	base.state.AppID = "fixture-pid-1"
	base.state.WindowID = "window-1"
	b := &queryFixtureBackend{fakeBackend: base, search: ElementQueryResult{Elements: namedElements()[:1], VisitedNodes: 7}}
	s := New(b, t.TempDir())
	t.Cleanup(s.unlockDesktop)
	return s, b
}
func query(t *testing.T, s *Service) Result {
	t.Helper()
	res := s.Call(context.Background(), "test-session", map[string]string{"action": "query", "text": "Send", "match_mode": "exact"})
	if !res.OK || res.Observation == nil {
		t.Fatalf("query: %+v", res)
	}
	return res
}
func TestQueryDoesNotCaptureAndAllowsOnlyElementInput(t *testing.T) {
	s, b := queryFixture(t)
	captures := 0
	b.captureHook = func(string) { captures++ }
	q := query(t, s)
	if captures != 0 || q.Observation.Path != "" || q.Observation.SHA256 != "" || len(q.Observation.Elements) != 1 || q.Observation.Query.VisitedNodes != 7 {
		t.Fatalf("query fabricated/captured image: %+v captures=%d", q, captures)
	}
	if b.request.MatchMode != "exact" || b.request.Limit != 20 || b.request.MaxNodes != 500 || b.request.ExpectAppID != b.state.AppID {
		t.Fatalf("query contract not forwarded: %+v", b.request)
	}
	for _, args := range []map[string]string{
		{"action": "click", "x": "50", "y": "25"},
		{"action": "type", "text": "hello"},
		{"action": "key", "key": "ENTER"},
		{"action": "scroll", "amount": "1"},
	} {
		args["observation_id"] = q.Observation.ID
		args["action_id"] = "query-block-" + args["action"]
		got := s.Call(context.Background(), "test-session", args)
		if got.Category != "screenshot_required" || b.calls != 0 {
			t.Fatalf("query authorized unrelated input: %+v", got)
		}
	}
	args := map[string]string{"action": "click_element", "element_index": "0", "observation_id": q.Observation.ID, "action_id": "query-click-01"}
	got := s.Call(context.Background(), "test-session", args)
	if !got.OK || !got.InputDispatched || got.InputMethod != "accessibility" || got.Observation.Path == "" || captures != 1 || b.calls != 1 {
		t.Fatalf("semantic action/readback: %+v captures=%d calls=%d", got, captures, b.calls)
	}
	if b.last.ExpectFrame != [4]int{-100, 10, 50, 25} || b.last.ExpectAppID != b.state.AppID || b.last.ExpectWindowID != b.state.WindowID {
		t.Fatalf("target binding: %+v", b.last)
	}
	if replay := s.Call(context.Background(), "test-session", args); !replay.OK || b.calls != 1 || captures != 1 {
		t.Fatalf("query input replayed: %+v", replay)
	}
}
func TestQueryEmptyAndErrorInvalidateOldObservation(t *testing.T) {
	s, b := queryFixture(t)
	old := observe(t, s)
	b.search.Elements = nil
	b.search.Truncated = true
	b.search.StopReason = "max_nodes"
	empty := query(t, s)
	if !empty.Observation.Query.Truncated || empty.Observation.Query.StopReason != "max_nodes" || len(empty.Observation.Elements) != 0 {
		t.Fatalf("lost completeness: %+v", empty)
	}
	if got := s.Call(context.Background(), "test-session", clickArgs(old)); got.Category != "stale_observation" {
		t.Fatalf("empty query kept old screenshot: %+v", got)
	}
	b.queryError = errors.New("permission revoked")
	got := s.Call(context.Background(), "test-session", map[string]string{"action": "query", "role": "Button"})
	if got.Category != "query_failed" || s.observation != nil {
		t.Fatalf("failed query kept targets: %+v", got)
	}
}
func TestQueryOwnershipAndWindowChange(t *testing.T) {
	s, b := queryFixture(t)
	q := query(t, s)
	args := map[string]string{"action": "query", "text": "Send"}
	if got := s.Call(context.Background(), "other-session", args); got.Category != "desktop_owned" || s.observation.ID != q.Observation.ID {
		t.Fatalf("query stole desktop: %+v", got)
	}
	b.queryHook = func() { b.state.WindowID = "window-2" }
	if got := s.Call(context.Background(), "test-session", args); got.Category != "desktop_changed" || s.observation != nil {
		t.Fatalf("query mixed windows: %+v", got)
	}
}
func TestQueryUnsupportedBackendDoesNotKeepOldFrame(t *testing.T) {
	s, _ := fixture(t)
	observe(t, s)
	got := s.Call(context.Background(), "test-session", map[string]string{"action": "query", "role": "Button"})
	if got.Category != "unsupported" || s.observation != nil {
		t.Fatalf("unsupported retained frame: %+v", got)
	}
}
func TestQueryArgumentBounds(t *testing.T) {
	for _, args := range []map[string]string{
		{}, {"text": strings.Repeat("中", 161)}, {"text": "a\x00b"}, {"role": strings.Repeat("R", 81)},
		{"text": "Send", "limit": "0"}, {"text": "Send", "limit": "81"}, {"text": "Send", "max_nodes": "2001"},
		{"text": "Send", "max_nodes": "x"}, {"text": "Send", "match_mode": "fuzzy"},
	} {
		if _, err := parseQuery(args); err == nil {
			t.Fatalf("accepted invalid query: %+v", args)
		}
	}
	if _, err := parseQuery(map[string]string{"role": "Button", "limit": "80", "max_nodes": "2000"}); err != nil {
		t.Fatal(err)
	}
}
func TestElementMetadataSecureAndPrivateFrame(t *testing.T) {
	var el Element
	err := json.Unmarshal([]byte(`{"index":0,"role":"AXTextField","name":"Password","value":"secret","secure":true,"settable":true,"enabled":false,"identity":"id-01","path":"0/1","x":-100,"y":10,"w":20,"h":10}`), &el)
	if err != nil || el.Value != "" || el.Settable || el.Enabled == nil || *el.Enabled || el.frame != [4]int{-100, 10, 20, 10} {
		t.Fatalf("bad metadata: %+v %v", el, err)
	}
	raw, _ := json.Marshal(el)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "0/1") || strings.Contains(string(raw), "frame") {
		t.Fatalf("private metadata leaked: %s", raw)
	}
}
func TestElementRightClickPreservesButtonAndSmallTreeWorks(t *testing.T) {
	s, b := fixture(t)
	b.elements = namedElements()[:1]
	o := observe(t, s)
	if o.Grounding != "element" {
		t.Fatal("one named control should be usable")
	}
	got := s.Call(context.Background(), "test-session", map[string]string{"action": "click_element", "button": "right", "element_index": "0", "observation_id": o.ID, "action_id": "right-click-01"})
	if !got.OK || b.last.Action != "click_element" || b.last.Button != "right" || got.InputMethod != "pointer" {
		t.Fatalf("right click became primary action: %+v %+v", got, b.last)
	}
}

func TestValueOnlyControlKeepsSemanticTarget(t *testing.T) {
	s, b := fixture(t)
	b.elements = []Element{{Index: 0, Role: "AXTextField", Value: "现有内容", Settable: true, Path: "0", X: -90, Y: 15, W: 20, H: 10}}
	o := observe(t, s)
	if o.Grounding != "element" || len(o.Elements) != 1 {
		t.Fatalf("value-only control discarded: %+v", o)
	}
	got := s.Call(context.Background(), "test-session", map[string]string{"action": "set_value", "element_index": "0", "text": "新内容", "observation_id": o.ID, "action_id": "value-only-01"})
	if !got.OK || b.last.ExpectValue != "现有内容" || b.last.Text != "新内容" || b.last.ExpectFrame != [4]int{-90, 15, 20, 10} {
		t.Fatalf("value/geometry binding lost: %+v %+v", got, b.last)
	}
}
func TestDisabledAndSecureTargetsNeverInject(t *testing.T) {
	for _, secure := range []bool{false, true} {
		s, b := fixture(t)
		b.elements = namedElements()
		disabled := false
		if secure {
			b.elements[0].Secure = true
		} else {
			b.elements[0].Enabled = &disabled
		}
		o := observe(t, s)
		got := s.Call(context.Background(), "test-session", map[string]string{"action": "click_element", "element_index": "0", "observation_id": o.ID, "action_id": "disabled-click-01"})
		if got.OK || b.calls != 0 {
			t.Fatalf("unavailable target injected: %+v", got)
		}
	}
}

func TestDesktopChangeDuringElementCollectionRejectsWholeFrame(t *testing.T) {
	s, b := fixture(t)
	old := observe(t, s)
	b.elements = namedElements()
	b.elementsHook = func() { b.state.Foreground = "window-2" }
	got := s.Call(context.Background(), "test-session", map[string]string{"action": "observe"})
	if got.Category != "desktop_changed" || got.Observation != nil || s.observation != nil {
		t.Fatalf("old image bound to new tree: %+v", got)
	}
	if got := s.Call(context.Background(), "test-session", clickArgs(old)); got.Category != "stale_observation" || b.calls != 0 {
		t.Fatalf("failed tree refresh retained old frame: %+v", got)
	}
}

func TestQueryExpirationAndCancellation(t *testing.T) {
	s, b := queryFixture(t)
	q := query(t, s)
	s.now = func() time.Time { return q.Observation.At.Add(61 * time.Second) }
	args := map[string]string{"action": "click_element", "element_index": "0", "observation_id": q.Observation.ID, "action_id": "expired-query-01"}
	if got := s.Call(context.Background(), "test-session", args); got.Category != "stale_observation" || b.calls != 0 {
		t.Fatalf("expired query injected: %+v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.queryHook = cancel
	if got := s.Call(ctx, "test-session", map[string]string{"action": "query", "text": "Send"}); got.Category != "cancelled" || s.observation != nil {
		t.Fatalf("cancelled query issued targets: %+v", got)
	}
}
