package desktop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// ElementQueryResult describes completeness within the configured search
// budget. Zero matches with Truncated=true never proves a control is absent.
type ElementQueryResult struct {
	Elements     []Element `json:"-"`
	VisitedNodes int       `json:"visited_nodes"`
	Truncated    bool      `json:"truncated"`
	StopReason   string    `json:"stop_reason,omitempty"`
}

type QueryBackend interface {
	Query(context.Context, Request) (ElementQueryResult, error)
}

func parseQuery(args map[string]string) (Request, error) {
	r := Request{Action: "query", Text: strings.TrimSpace(args["text"]), Role: strings.TrimSpace(args["role"]), MatchMode: args["match_mode"], Limit: 20, MaxNodes: 500}
	if r.Text == "" && r.Role == "" {
		return r, fmt.Errorf("query requires text and/or role")
	}
	if len([]rune(r.Text)) > 160 || len([]rune(r.Role)) > 80 || strings.ContainsRune(r.Text+r.Role, 0) {
		return r, fmt.Errorf("query text/role must be bounded (160/80 characters) without NUL")
	}
	if r.MatchMode == "" {
		r.MatchMode = "contains"
	}
	if r.MatchMode != "contains" && r.MatchMode != "exact" {
		return r, fmt.Errorf("match_mode must be contains or exact; exact never falls back to contains")
	}
	for _, field := range []struct {
		name string
		dst  *int
		max  int
	}{{"limit", &r.Limit, 80}, {"max_nodes", &r.MaxNodes, 2000}} {
		if raw, ok := args[field.name]; ok {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > field.max {
				return r, fmt.Errorf("%s must be 1..%d", field.name, field.max)
			}
			*field.dst = n
		}
	}
	return r, nil
}

func (s *Service) query(ctx context.Context, session string, args map[string]string) Result {
	req, err := parseQuery(args)
	if err != nil {
		return failure("invalid_arguments", err)
	}
	// Even an empty/failed new search invalidates the old targets.
	s.observation = nil
	b, ok := s.backend.(QueryBackend)
	if !ok {
		return failure("unsupported", fmt.Errorf("targeted accessibility query unavailable; use observe"))
	}
	if err = s.lockDesktop(); err != nil {
		return failure("desktop_owned", err)
	}
	keep := false
	defer func() {
		if !keep && s.owner == "" {
			s.unlockDesktop()
		}
	}()
	before, err := s.backend.Status(ctx)
	if err != nil {
		return failure("unavailable", err)
	}
	if !before.Ready || before.Width <= 0 || before.Height <= 0 {
		return failure("unavailable", fmt.Errorf("desktop unavailable: %s", before.Reason))
	}
	req.ExpectAppID, req.ExpectWindowID = before.AppID, before.WindowID
	search, err := b.Query(ctx, req)
	if err != nil {
		return failure("query_failed", err)
	}
	after, err := s.backend.Status(ctx)
	if err != nil {
		return failure("unavailable", err)
	}
	if !sameDesktop(before, after) {
		return failure("desktop_changed", fmt.Errorf("desktop changed during query; query again"))
	}
	if err = ctx.Err(); err != nil {
		return failure("cancelled", err)
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return failure("artifact_failed", err)
	}
	// No image is fabricated. The only permissible input from this observation
	// is an element action, which revalidates the live native target.
	obs := &Observation{ID: hex.EncodeToString(id), Width: after.Width, Height: after.Height, CoordinateSpace: "desktop_points", Grounding: "element", GroundingNote: "query has no screenshot; only element actions are allowed; exact matching never falls back", At: s.now(), State: after, Query: &search}
	obs.Elements = scaleElements(search.Elements, after, after.Width, after.Height)
	s.observation = obs
	s.owner = session
	s.renewLease()
	keep = true
	return Result{OK: true, Observation: obs, RetrySafe: true}
}
