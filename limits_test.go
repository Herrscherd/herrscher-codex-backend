package codex

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestLimitsReadsThePlanWindowsUnderTheProfileHome(t *testing.T) {
	fake := installFakeCodex(t)
	fake.write(t, "ratelimits.json", `{"id":1,"result":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":100,"windowDurationMins":43200,"resetsAt":1796083651},"secondary":null,"planType":"go","rateLimitReachedType":"rate_limit_reached"},"rateLimitsByLimitId":{"codex":{"limitId":"codex","limitName":null,"primary":{"usedPercent":100,"windowDurationMins":43200,"resetsAt":1796083651},"secondary":null}},"rateLimitResetCredits":{"availableCount":2,"credits":null}}}`)
	limits, err := readLimits(t.Context(), fake.access("/profiles/work"))
	if err != nil {
		t.Fatal(err)
	}
	want := []contracts.Limit{{Window: contracts.LimitMonthly, UsedPercent: 100, ResetsAt: time.Unix(1796083651, 0), Span: 30 * 24 * time.Hour}}
	if !slices.Equal(limits, want) {
		t.Fatalf("limits = %+v, want %+v", limits, want)
	}
	if calls := fake.read(t, "calls"); calls != "app-server --listen stdio://|CODEX_HOME=/profiles/work\n" {
		t.Fatalf("calls = %q, want one app-server under the profile home", calls)
	}
}

func TestLimitsScopesEveryBucketButTheAccountOne(t *testing.T) {
	fake := installFakeCodex(t)
	fake.write(t, "ratelimits.json", `{"id":1,"result":{"rateLimits":{},"rateLimitsByLimitId":{"codex":{"primary":{"usedPercent":12,"windowDurationMins":300},"secondary":{"usedPercent":130,"windowDurationMins":10080}},"codex_spark":{"limitId":"codex_spark","limitName":"Spark","primary":{"usedPercent":-4,"windowDurationMins":300}},"codex_other":{"limitId":"codex_other","primary":{"usedPercent":5,"windowDurationMins":60}}}}}`)
	limits, err := readLimits(t.Context(), fake.access(""))
	if err != nil {
		t.Fatal(err)
	}
	want := []contracts.Limit{
		{Window: contracts.LimitFiveHour, UsedPercent: 12, Span: 5 * time.Hour},
		{Window: contracts.LimitWeekly, UsedPercent: 100, Span: 7 * 24 * time.Hour},
		{Window: contracts.LimitFiveHour, Scope: "Spark", UsedPercent: 0, Span: 5 * time.Hour},
	}
	if !slices.Equal(limits, want) {
		t.Fatalf("limits = %+v, want %+v", limits, want)
	}
}

func TestLimitsUnavailableWhenCodexRefusesTheRead(t *testing.T) {
	fake := installFakeCodex(t)
	fake.write(t, "ratelimits.json", `{"id":1,"error":{"code":-32600,"message":"codex account authentication required to read rate limits"}}`)
	_, err := readLimits(t.Context(), fake.access(""))
	if !errors.Is(err, contracts.ErrLimitsUnavailable) {
		t.Fatalf("err = %v, want ErrLimitsUnavailable", err)
	}
}

func TestLimitsUnavailableWhenNoWindowIsKnown(t *testing.T) {
	fake := installFakeCodex(t)
	fake.write(t, "ratelimits.json", `{"id":1,"result":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":5,"windowDurationMins":60}}}}`)
	_, err := readLimits(t.Context(), fake.access(""))
	if !errors.Is(err, contracts.ErrLimitsUnavailable) {
		t.Fatalf("err = %v, want ErrLimitsUnavailable", err)
	}
}

func TestLimitsMissingBinaryIsAFailureNotAnAbsence(t *testing.T) {
	access := installFakeCodex(t).access("")
	access.Config.Settings["cmd"] = filepath.Join(t.TempDir(), "absent")
	_, err := readLimits(t.Context(), access)
	if err == nil || errors.Is(err, contracts.ErrLimitsUnavailable) {
		t.Fatalf("err = %v, want a start failure", err)
	}
}
