package sqlite

import (
	"cpa-key-billing/internal/billing"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGroupedLedgerAndLabelsRoundTrip(t *testing.T) {
	d := openTestDB(t)
	state := billing.NewState()
	now := time.Now().UTC()
	scope := billing.CallerScope("dummy-group-key")
	state.Keys[scope] = &billing.KeyState{Preview: "dummy…key", InConfig: true, Cycles: map[string]billing.QuotaCycle{}}
	publish := now.Add(-time.Hour)
	limit := billing.USD(500_000_000_000)
	concurrency := 2
	group := billing.ResourceGroup{ID: "arbitrary-group", Name: "可变名称", Status: "published", Enabled: true, Revision: 2, Models: []string{"custom/model"}, Pool: billing.GroupPool{Mode: "inherit"}, CreatedAt: publish, UpdatedAt: now, PublishedAt: &publish}
	state.Grouped.Groups[group.ID] = group
	plan := billing.GroupPlan{ID: "custom-plan", Name: "任意计划", Revision: 1, CreatedAt: now, UpdatedAt: now, Policies: []billing.GroupPolicy{{GroupID: group.ID, Enabled: true, DailyLimit: &limit, ConcurrencyLimit: &concurrency}}}
	state.Grouped.Plans[plan.ID] = plan
	state.Grouped.Bindings[scope] = billing.GroupKeyBinding{Scope: scope, Revision: 1, Unclassified: 2, Periods: []billing.GroupBindingPeriod{{Mode: "grouped", PlanID: &plan.ID, EffectiveFrom: publish}}}
	day := billing.GroupDay{Scope: scope, GroupID: group.ID, Date: "2026-09-17", Raw: 1234567891, Credit: 1, Tokens: 11, Requests: 2, ResetCutoff: &now, UpdatedAt: now, AccountingError: "overflow"}
	state.Grouped.Days[day.Key()] = day
	label := billing.SharedLabel{InstanceID: "keeper-instance", Kind: "downstream_key", SubjectID: scope, ExternalID: 21, Value: "统一备注", FetchedAt: now}
	state.Grouped.Labels[label.Key()] = label
	audit := billing.GroupAudit{At: now, ActorKind: "management-key", Action: "create", ObjectType: "group", ObjectID: group.ID, Before: json.RawMessage(`null`), After: json.RawMessage(`{}`), Result: "success"}
	mustSave(t, d, state, billing.Changes{Keys: []string{scope}, GroupedConfig: true, GroupDays: []string{day.Key()}, GroupLabels: true, GroupAudits: []billing.GroupAudit{audit}})
	got := mustLoad(t, d).State.Grouped
	if got.Days[day.Key()].Raw != day.Raw || got.Days[day.Key()].Credit != 1 || got.Days[day.Key()].AccountingError != "overflow" || got.Bindings[scope].Unclassified != 2 {
		t.Fatal("ledger loss", got)
	}
	if got.Labels[label.Key()].Value != label.Value || got.Plans[plan.ID].Policies[0].DailyLimit == nil || *got.Plans[plan.ID].Policies[0].DailyLimit != limit {
		t.Fatal("configuration loss", got)
	}
	rows, err := d.GroupAuditPage(0, 51, "group", group.ID)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	if _, err = d.db.Exec(`UPDATE gb_group_models SET model_key='bad-model' WHERE group_id=?`, group.ID); err == nil {
		t.Fatal("published model mutation accepted")
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "dummy-group-key") {
		t.Fatal("plaintext key persisted")
	}
}
