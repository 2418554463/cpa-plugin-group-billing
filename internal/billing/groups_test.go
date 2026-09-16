package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func groupTestStore(t *testing.T) (*Store, *memoryRepository, *time.Time, string, ResourceGroup, ResourceGroup) {
	t.Helper()
	s, repo := newStoreWithRepository(t)
	now := time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	scope := CallerScope("dummy-group-customer")
	s.ReplaceAll(func(st *State) { st.Keys[scope] = &KeyState{InConfig: true, Cycles: map[string]QuotaCycle{}} })
	groups := []ResourceGroup{}
	for _, name := range []string{"任意高能力", "任意日常"} {
		g, err := s.CreateResourceGroup(ResourceGroup{Name: name, Models: []string{"example/" + name}, Pool: GroupPool{Mode: "inherit"}})
		if err != nil {
			t.Fatal(err)
		}
		g, err = s.PublishResourceGroup(g.ID, g.Revision, "测试发布", false)
		if err != nil {
			t.Fatal(err)
		}
		groups = append(groups, g)
		if _, err := s.UpsertPrice(CustomPrice{ModelID: g.Models[0], PriceRates: PriceRates{OutputPer1M: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	amount := USD(2_000_000_000)
	slots := 2
	plan, err := s.SaveGroupPlan(GroupPlan{Name: "任意套餐", Policies: []GroupPolicy{{GroupID: groups[0].ID, Enabled: true, DailyLimit: &amount, ConcurrencyLimit: &slots}, {GroupID: groups[1].ID, Enabled: true}}}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ScheduleGroupBinding(scope, "grouped", &plan.ID, 0, "测试绑定"); err != nil {
		t.Fatal(err)
	}
	if s.IsGrouped(scope, now) {
		t.Fatal("binding must not activate before midnight")
	}
	now = nextGroupDay(now).Add(time.Minute)
	return s, repo, &now, scope, groups[0], groups[1]
}
func groupTestUsage(s *Store, scope, model string, at time.Time, tokens int64) {
	s.RecordUsage(UsageEvent{Scope: scope, RouteModel: model, UpstreamModel: model, At: s.Now(), RequestedAt: at, Breakdown: completeBreakdown(0, 0, 0, tokens, 0)})
}

func TestGroupMoneyExactJSON(t *testing.T) {
	for _, value := range []string{"0", "1", "500", "0.000000001", "9223372036.854775807"} {
		v, err := ParseUSD(value)
		if err != nil {
			t.Fatal(err)
		}
		if v.String() != value {
			t.Fatalf("%s != %s", v.String(), value)
		}
		raw, _ := json.Marshal(v)
		var again USD
		if err := json.Unmarshal(raw, &again); err != nil || again != v {
			t.Fatalf("roundtrip %s: %v", value, err)
		}
	}
	for _, bad := range []string{"-1", "1e9", "NaN", "1.0000000001", "9223372036.854775808", "01", ".1", "1.", " 1"} {
		if _, err := ParseUSD(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), -1, 1e20} {
		if _, ok := costUSD(bad); ok {
			t.Fatalf("accepted float %v", bad)
		}
	}
	var value USD
	if err := json.Unmarshal([]byte(`500`), &value); err == nil {
		t.Fatal("accepted numeric USD")
	}
}

func TestGroupPolicyRequiresExplicitUnlimited(t *testing.T) {
	for _, raw := range []string{`{"group_id":"g","enabled":true}`, `{"group_id":"g","enabled":null,"daily_limit_usd":null,"concurrency_limit":null}`, `{"group_id":"g","enabled":true,"daily_limit_usd":500,"concurrency_limit":2}`, `{"group_id":"g","enabled":true,"daily_limit_usd":null,"concurrency_limit":null,"typo":1}`} {
		var p GroupPolicy
		if err := json.Unmarshal([]byte(raw), &p); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	var p GroupPolicy
	if err := json.Unmarshal([]byte(`{"group_id":"g","enabled":true,"daily_limit_usd":null,"concurrency_limit":null}`), &p); err != nil {
		t.Fatal(err)
	}
}

func TestGroupAliasNoGuessAndPlanEditNoRefill(t *testing.T) {
	s, _, now, scope, a, _ := groupTestStore(t)
	groupTestUsage(s, scope, a.Models[0], *now, 1_000_000)
	plan := s.GroupPlans()[0]
	higher := USD(5_000_000_000)
	plan.Policies[0].DailyLimit = &higher
	if _, err := s.SaveGroupPlan(plan, plan.Revision, "更改政策"); err != nil {
		t.Fatal(err)
	}
	for _, v := range s.GroupUsage(scope).Groups {
		if v.GroupID == a.ID && v.Used != 1_000_000_000 {
			t.Fatal("plan edit refilled quota", v)
		}
	}
	s.RecordUsage(UsageEvent{Scope: scope, RouteModel: "unknown/alias", UpstreamModel: a.Models[0], At: *now, RequestedAt: *now, Breakdown: completeBreakdown(0, 0, 0, 1_000_000, 0)})
	if s.GroupUsage(scope).Unclassified != 1 {
		t.Fatal("guessed an alias from upstream model")
	}
	for _, v := range s.GroupUsage(scope).Groups {
		if v.GroupID == a.ID && v.Used != 1_000_000_000 {
			t.Fatal("unclassified charged an unrelated group", v)
		}
	}
	if d := s.AdmitGroup(scope, "suffix", a.Models[0], a.Models[0]+"(high)", true); !d.Allowed {
		t.Fatal("CPA thinking option rejected", d)
	}
	s.ReleaseSlot("suffix")
}

func TestGroupLedgerOverflowClosesAdmission(t *testing.T) {
	s, _, now, scope, a, _ := groupTestStore(t)
	s.ReplaceAll(func(state *State) {
		d := GroupDay{Scope: scope, GroupID: a.ID, Date: groupDate(*now), Raw: USD(math.MaxInt64), Credit: USD(math.MaxInt64)}
		state.Grouped.Days[d.Key()] = d
	})
	groupTestUsage(s, scope, a.Models[0], *now, 1_000_000)
	if d := s.AdmitGroup(scope, "overflow", a.Models[0], "", true); d.Code != "accounting_unavailable" {
		t.Fatal(d)
	}
	for _, v := range s.GroupUsage(scope).Groups {
		if v.GroupID == a.ID && (v.Raw != USD(math.MaxInt64) || v.Incomplete != 1) {
			t.Fatal(v)
		}
	}
}
func TestGroupIndependentQuotaConcurrencyAndKeys(t *testing.T) {
	s, _, now, scope, a, b := groupTestStore(t)
	for i := 0; i < 2; i++ {
		if d := s.AdmitGroup(scope, fmt.Sprint(i), a.Models[0], a.Models[0], true); !d.Allowed {
			t.Fatalf("admit: %+v", d)
		}
	}
	if d := s.AdmitGroup(scope, "third", a.Models[0], a.Models[0], true); d.Code != "group_concurrency_exceeded" {
		t.Fatalf("third: %+v", d)
	}
	if d := s.AdmitGroup(scope, "daily", b.Models[0], b.Models[0], true); !d.Allowed {
		t.Fatalf("B affected: %+v", d)
	}
	if d := s.AdmitGroup(scope, "0", a.Models[0], a.Models[0], true); !d.Allowed {
		t.Fatalf("duplicate: %+v", d)
	}
	if !s.ReleaseSlot("0") || s.ReleaseSlot("0") {
		t.Fatal("release not idempotent")
	}
	groupTestUsage(s, scope, a.Models[0], *now, 2_000_000)
	if d := s.AdmitGroup(scope, "over", a.Models[0], a.Models[0], true); d.Code != "group_quota_exceeded" {
		t.Fatalf("quota: %+v", d)
	}
	if d := s.AdmitGroup(scope, "more-b", b.Models[0], b.Models[0], true); !d.Allowed {
		t.Fatalf("B affected by A quota: %+v", d)
	}
	other := CallerScope("dummy-second-customer")
	s.ReplaceAll(func(st *State) {
		st.Keys[other] = &KeyState{InConfig: true}
		binding := st.Grouped.Bindings[scope]
		binding.Scope = other
		st.Grouped.Bindings[other] = binding
	})
	if d := s.AdmitGroup(other, "other", a.Models[0], a.Models[0], true); !d.Allowed {
		t.Fatalf("other key affected: %+v", d)
	}
}
func TestGroupConcurrentSlotRace(t *testing.T) {
	s, _, _, scope, a, _ := groupTestStore(t)
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if s.AdmitGroup(scope, fmt.Sprintf("race-%d", i), a.Models[0], "", true).Allowed {
				accepted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 2 {
		t.Fatalf("accepted %d", accepted.Load())
	}
	for i := 0; i < 60; i++ {
		s.ReleaseSlot(fmt.Sprintf("race-%d", i))
	}
	if got := s.GroupUsage(scope).Groups[0].Active; got != 0 {
		t.Fatalf("leaked %d slots", got)
	}
}
func TestGroupLateUsageAndResetKeepRawLedger(t *testing.T) {
	s, _, now, scope, a, _ := groupTestStore(t)
	start := *now
	groupTestUsage(s, scope, a.Models[0], start, 1_000_000)
	*now = now.Add(time.Minute)
	if err := s.ResetGroupDay(scope, a.ID, "测试重置"); err != nil {
		t.Fatal(err)
	}
	groupTestUsage(s, scope, a.Models[0], start, 1_000_000)
	v := s.GroupUsage(scope).Groups[0]
	if v.Raw != USD(2e9) || v.Used != 0 {
		t.Fatalf("reset late usage: %+v", v)
	}
	oldDate := groupDate(*now)
	*now = nextGroupDay(*now).Add(time.Minute)
	groupTestUsage(s, scope, a.Models[0], start, 1_000_000)
	if v := s.GroupUsage(scope).Groups[0]; v.Used != 0 {
		t.Fatalf("late usage charged new day: %+v", v)
	}
	old := s.state.Grouped.Days[(GroupDay{Scope: scope, GroupID: a.ID, Date: oldDate}).Key()]
	if old.Raw != USD(3e9) || old.Credit != USD(3e9) {
		t.Fatalf("old day not updated: %+v", old)
	}
}
func TestGroupConfigConflictAndPersistenceFailure(t *testing.T) {
	s, repo, now, scope, a, _ := groupTestStore(t)
	dup, err := s.CreateResourceGroup(ResourceGroup{Name: "重复", Models: a.Models, Pool: GroupPool{Mode: "inherit"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishResourceGroup(dup.ID, dup.Revision, "不应通过", false); KindOf(err) != KindConflict {
		t.Fatalf("duplicate model error %v", err)
	}
	models := []string{"example/new"}
	if _, err := s.UpdateResourceGroup(GroupPatch{GroupID: a.ID, ExpectedRevision: a.Revision, Models: &models, Reason: "不应改已发布成员"}); KindOf(err) != KindConflict {
		t.Fatalf("mutable published members: %v", err)
	}
	name := "改名"
	if _, err := s.UpdateResourceGroup(GroupPatch{GroupID: a.ID, ExpectedRevision: a.Revision - 1, Name: &name, Reason: "过期修订"}); KindOf(err) != KindConflict {
		t.Fatalf("stale revision accepted: %v", err)
	}
	repo.fail = errors.New("test disk failure")
	if _, err := s.UpdateResourceGroup(GroupPatch{GroupID: a.ID, ExpectedRevision: a.Revision, Name: &name, Reason: "保存失败"}); err == nil {
		t.Fatal("write failure hidden")
	}
	if s.ResourceGroups()[0].Name != a.Name {
		t.Fatal("failed configuration was published")
	}
	groupTestUsage(s, scope, a.Models[0], *now, 1_000_000)
	if d := s.AdmitGroup(scope, "blocked", a.Models[0], "", true); d.Code != "accounting_unavailable" {
		t.Fatalf("failed storage did not close admission: %+v", d)
	}
	repo.fail = nil
	if d := s.AdmitGroup(scope, "recovered", a.Models[0], "", true); !d.Allowed {
		t.Fatalf("pending write not recovered: %+v", d)
	}
}
func TestGroupUnlimitedStillCountsAndDynamicThirdGroup(t *testing.T) {
	s, _, now, scope, _, b := groupTestStore(t)
	groupTestUsage(s, scope, b.Models[0], *now, 3_000_000)
	view := s.GroupUsage(scope)
	if view.Groups[1].Used != USD(3e9) || view.Groups[1].Remaining != nil {
		t.Fatalf("unlimited not counted: %+v", view)
	}
	c, err := s.CreateResourceGroup(ResourceGroup{Name: "第三种任意业务", Models: []string{"brand-new/provider-model"}, Pool: GroupPool{Mode: "inherit"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.PublishResourceGroup(c.ID, c.Revision, "测试任意组", false)
	if err != nil {
		t.Fatal(err)
	}
	if d := s.AdmitGroup(scope, "unassigned", c.Models[0], "", true); d.Code != "group_disabled" {
		t.Fatalf("new group implicitly opened: %+v", d)
	}
}
