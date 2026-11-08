package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	rateLimitsMethod  = "account/rateLimits/read"
	accountLimitID    = "codex"
	fiveHourSpan      = 5 * time.Hour
	weekSpan          = 7 * 24 * time.Hour
	shortestMonthSpan = 28 * 24 * time.Hour
	longestMonthSpan  = 31 * 24 * time.Hour
)

type rateLimitsResponse struct {
	RateLimits rateLimitSnapshot            `json:"rateLimits"`
	ByLimitID  map[string]rateLimitSnapshot `json:"rateLimitsByLimitId"`
}

type rateLimitSnapshot struct {
	LimitID   string           `json:"limitId"`
	LimitName string           `json:"limitName"`
	Primary   *rateLimitWindow `json:"primary"`
	Secondary *rateLimitWindow `json:"secondary"`
}

type rateLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int64   `json:"windowDurationMins"`
	ResetsAt           int64   `json:"resetsAt"`
}

func readLimits(ctx context.Context, access contracts.ProbeAccess) ([]contracts.Limit, error) {
	return probeTargetOf(access).limits(ctx)
}

func (t probeTarget) limits(ctx context.Context) ([]contracts.Limit, error) {
	var response rateLimitsResponse
	err := callAppServer(ctx, t.argv, t.env, func(session *rpcSession) error {
		raw, err := session.request(appServerCallID, rateLimitsMethod, map[string]any{"excludeResetCreditDetails": true})
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			return fmt.Errorf("decode %s: %w", rateLimitsMethod, err)
		}
		return nil
	})
	if errors.Is(err, errCodexRPC) {
		return nil, fmt.Errorf("%w: %w", contracts.ErrLimitsUnavailable, err)
	}
	if err != nil {
		return nil, err
	}
	limits := response.limits()
	if len(limits) == 0 {
		return nil, contracts.ErrLimitsUnavailable
	}
	return limits, nil
}

func (r rateLimitsResponse) limits() []contracts.Limit {
	var limits []contracts.Limit
	for _, snapshot := range r.snapshots() {
		for _, window := range []*rateLimitWindow{snapshot.Primary, snapshot.Secondary} {
			if limit, known := window.limit(snapshot.scope()); known {
				limits = append(limits, limit)
			}
		}
	}
	return limits
}

func (r rateLimitsResponse) snapshots() []rateLimitSnapshot {
	if len(r.ByLimitID) == 0 {
		return []rateLimitSnapshot{r.RateLimits}
	}
	snapshots := make([]rateLimitSnapshot, 0, len(r.ByLimitID))
	for _, limitID := range slices.Sorted(maps.Keys(r.ByLimitID)) {
		snapshot := r.ByLimitID[limitID]
		if snapshot.LimitID == "" {
			snapshot.LimitID = limitID
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots
}

func (s rateLimitSnapshot) scope() string {
	switch {
	case s.LimitID == "" || s.LimitID == accountLimitID:
		return ""
	case s.LimitName != "":
		return s.LimitName
	}
	return s.LimitID
}

func (w *rateLimitWindow) limit(scope string) (contracts.Limit, bool) {
	if w == nil {
		return contracts.Limit{}, false
	}
	span := time.Duration(w.WindowDurationMins) * time.Minute
	window, known := limitWindowOf(span)
	if !known {
		return contracts.Limit{}, false
	}
	limit := contracts.Limit{Window: window, Scope: scope, UsedPercent: min(max(w.UsedPercent, 0), fullyUsedPercent), Span: span}
	if w.ResetsAt > 0 {
		limit.ResetsAt = time.Unix(w.ResetsAt, 0)
	}
	return limit, true
}

func limitWindowOf(span time.Duration) (contracts.LimitWindow, bool) {
	switch {
	case span == fiveHourSpan:
		return contracts.LimitFiveHour, true
	case span == weekSpan:
		return contracts.LimitWeekly, true
	case span >= shortestMonthSpan && span <= longestMonthSpan:
		return contracts.LimitMonthly, true
	}
	return 0, false
}
