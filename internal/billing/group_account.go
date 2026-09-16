package billing

import (
	"sort"
	"strings"
	"time"
)

type groupSlot struct{ Scope, GroupID string }

func groupSlotKey(scope, groupID string) string { return scope + "\x00" + groupID }

type GroupDecision struct {
	Handled bool
	Allowed bool
	Code    string
	GroupID string
	Message string
	Status  int
	RetryAt time.Time
}

// Admission and slot acquisition share Store.mu with usage accounting. No
// request/usage association is inferred: only lifecycle RequestID owns slots.
func (s *Store) AdmitGroup(scope, requestID, model, requested string, generate bool) GroupDecision {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	binding, grouped := s.state.Grouped.binding(scope, now)
	if !grouped {
		return GroupDecision{Allowed: true}
	}
	decision := GroupDecision{Handled: true, Status: 403}
	deny := func(code, message string, status int) GroupDecision {
		decision.Code = code
		decision.Message = message
		decision.Status = status
		return decision
	}
	// Retry pending synchronous writes before considering new spending. A broken
	// database cannot make quota checks silently run forever on stale disk state.
	if !s.dirty.empty() {
		if s.repo == nil || s.repo.Save(s.state, s.dirty) != nil {
			return deny("accounting_unavailable", "计费账本尚未保存，请稍后重试", 503)
		}
		s.dirty = Changes{}
	}
	if requested == "" {
		requested = model
	}
	group, ok := s.state.Grouped.requestModelOwner(requested, now)
	if !ok {
		return deny("model_not_grouped", "模型未归入已发布的资源组", 403)
	}
	decision.GroupID = group.ID
	if binding.PlanID == nil {
		return deny("group_disabled", "分组计划不可用", 403)
	}
	policy, ok := s.state.Grouped.policy(*binding.PlanID, group.ID)
	if !ok || !policy.Enabled || !group.Enabled || group.Status != "published" {
		return deny("group_disabled", "当前套餐未开放此资源组", 403)
	}
	day := s.state.Grouped.Days[(GroupDay{Scope: scope, GroupID: group.ID, Date: groupDate(now)}).Key()]
	if day.AccountingError != "" {
		return deny("accounting_unavailable", "本组存在金额溢出或核算错误，需要管理员对账", 503)
	}
	if policy.DailyLimit != nil && day.Raw-day.Credit >= *policy.DailyLimit {
		decision.RetryAt = nextGroupDay(now)
		return deny("group_quota_exceeded", "此资源组当日额度已用尽", 429)
	}
	if generate {
		requestID = strings.TrimSpace(requestID)
		if requestID == "" {
			return deny("accounting_unavailable", "宿主未提供生命周期请求 ID", 503)
		}
		if existing, ok := s.groupSlots[requestID]; ok {
			if existing.Scope != scope || existing.GroupID != group.ID {
				return deny("accounting_unavailable", "重复请求的计费主体或分组不一致", 503)
			}
			decision.Allowed = true
			return decision
		}
		if _, ok := s.activeRequests[requestID]; ok {
			return deny("accounting_unavailable", "请求已在旧计费模式准入", 503)
		}
		key := groupSlotKey(scope, group.ID)
		count := s.groupActive[key]
		if policy.ConcurrencyLimit != nil && count >= *policy.ConcurrencyLimit {
			return deny("group_concurrency_exceeded", "此资源组并发已满", 429)
		}
		s.groupSlots[requestID] = groupSlot{Scope: scope, GroupID: group.ID}
		s.groupActive[key] = count + 1
	}
	decision.Allowed = true
	decision.Status = 200
	return decision
}
func (s *Store) releaseGroupSlotLocked(requestID string) bool {
	slot, ok := s.groupSlots[requestID]
	if !ok {
		return false
	}
	delete(s.groupSlots, requestID)
	key := groupSlotKey(slot.Scope, slot.GroupID)
	if s.groupActive[key] <= 1 {
		delete(s.groupActive, key)
	} else {
		s.groupActive[key]--
	}
	return true
}

// GroupCredentialPolicy is an additional filter, never a union with a Key's
// existing route allow-list. The scheduler must require BOTH to allow a candidate.
func (s *Store) GroupCredentialPolicy(scope, model, requested string) (GroupPool, bool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.Now()
	b, grouped := s.state.Grouped.binding(scope, now)
	if !grouped {
		return GroupPool{}, false, true
	}
	if requested == "" {
		requested = model
	}
	g, ok := s.state.Grouped.requestModelOwner(requested, now)
	if !ok || b.PlanID == nil {
		return GroupPool{}, true, false
	}
	p, allowed := s.state.Grouped.policy(*b.PlanID, g.ID)
	return g.Pool, true, allowed && p.Enabled && g.Enabled && g.Status == "published"
}

// Called only from usage.handle through recordUsage while Store.mu is held.
// Late usage is applied to its original Shanghai date and binding interval.
func accountGroupUsage(state *State, event UsageEvent, entry *RequestEvent, changes *Changes) bool {
	at := event.RequestedAt
	if at.IsZero() {
		at = entry.At
	}
	binding, grouped := state.Grouped.binding(entry.Scope, at)
	if !grouped {
		return false
	}
	attr := &GroupAttribution{Classification: "unclassified", PricingStatus: "unpriced", Date: groupDate(at)}
	entry.Group = attr
	model := event.RouteModel
	if model == "" {
		model = event.UpstreamModel
	}
	attr.ModelKey = groupModel(model)
	group, ok := state.Grouped.requestModelOwner(model, at)
	if !ok || event.RequestedAt.IsZero() {
		attr.Reason = "model_or_start_time_missing"
		b := state.Grouped.Bindings[entry.Scope]
		b.Unclassified++
		state.Grouped.Bindings[entry.Scope] = b
		changes.GroupedConfig = true
		return true
	}
	attr.GroupID = group.ID
	attr.Classification = "grouped"
	attr.PublishedAt = group.PublishedAt
	d := GroupDay{Scope: entry.Scope, GroupID: group.ID, Date: groupDate(at)}
	key := d.Key()
	if old, exists := state.Grouped.Days[key]; exists {
		d = old
	}
	amount, valid := costUSD(entry.Cost.TotalUSD)
	if !valid {
		d.AccountingError = "amount_overflow"
		attr.Reason = "amount_overflow"
	}
	priced := valid && entry.PriceSource != PriceSourceNone && event.Breakdown.Billable()
	if priced {
		raw, okRaw := addGroupCount(int64(d.Raw), int64(amount))
		credit := int64(d.Credit)
		okCredit := true
		if d.ResetCutoff != nil && at.Before(*d.ResetCutoff) {
			credit, okCredit = addGroupCount(credit, int64(amount))
		}
		if okRaw && okCredit {
			d.Raw = USD(raw)
			d.Credit = USD(credit)
			attr.Cost = &amount
			attr.PricingStatus = "priced"
		} else {
			priced = false
			attr.Reason = "amount_overflow"
			d.AccountingError = "amount_overflow"
		}
	}
	if !priced {
		d.Incomplete++
		if attr.Reason == "" {
			attr.Reason = "usage_or_price_incomplete"
		}
	}
	if count, ok := addGroupCount(d.Requests, 1); ok {
		d.Requests = count
	} else {
		d.Incomplete++
	}
	if event.Breakdown.Valid() {
		if tokens, ok := addGroupCount(d.Tokens, event.Breakdown.TotalTokens); ok {
			d.Tokens = tokens
		} else {
			d.Incomplete++
		}
	}
	d.UpdatedAt = event.At
	state.Grouped.Days[key] = d
	changes.GroupDays = append(changes.GroupDays, key)
	_ = binding // The interval determines mode, not a second plan-scoped ledger.
	return true
}

type GroupUsageView struct {
	GroupID            string     `json:"group_id"`
	GroupName          string     `json:"group_name"`
	Enabled            bool       `json:"enabled"`
	Date               string     `json:"business_date"`
	DailyLimit         *USD       `json:"daily_limit_usd"`
	Raw                USD        `json:"raw_cost_usd"`
	Credit             USD        `json:"reset_credit_usd"`
	Used               USD        `json:"used_usd"`
	Remaining          *USD       `json:"remaining_usd"`
	Overage            USD        `json:"overage_usd"`
	ConcurrencyLimit   *int       `json:"concurrency_limit"`
	Active             int        `json:"active_requests"`
	Requests           int64      `json:"requests"`
	Tokens             int64      `json:"tokens"`
	Incomplete         int64      `json:"incomplete_records"`
	AccountingComplete bool       `json:"accounting_complete"`
	NextResetAt        time.Time  `json:"next_reset_at"`
	ResetCutoff        *time.Time `json:"reset_cutoff"`
	Admission          string     `json:"admission"`
}
type GroupKeyUsage struct {
	Scope        string           `json:"scope"`
	Label        string           `json:"label"`
	LabelStale   bool             `json:"label_stale"`
	Timezone     string           `json:"timezone"`
	PlanID       *string          `json:"plan_id"`
	Groups       []GroupUsageView `json:"groups"`
	Unclassified int64            `json:"unclassified_records"`
}

func (s *Store) GroupUsage(scope string) GroupKeyUsage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.Now()
	view := GroupKeyUsage{Scope: scope, Timezone: "Asia/Shanghai", Groups: []GroupUsageView{}}
	if key := s.state.Keys[scope]; key != nil {
		view.Label = key.Label
	}
	b, ok := s.state.Grouped.binding(scope, now)
	if !ok || b.PlanID == nil {
		return view
	}
	view.PlanID = b.PlanID
	view.Unclassified = s.state.Grouped.Bindings[scope].Unclassified
	for _, group := range s.state.Grouped.Groups {
		policy, exists := s.state.Grouped.policy(*b.PlanID, group.ID)
		d := s.state.Grouped.Days[(GroupDay{Scope: scope, GroupID: group.ID, Date: groupDate(now)}).Key()]
		if !exists && d.Requests == 0 {
			continue
		}
		item := GroupUsageView{GroupID: group.ID, GroupName: group.Name, Enabled: exists && policy.Enabled && group.Enabled && group.Status == "published", Date: groupDate(now), DailyLimit: policy.DailyLimit, Raw: d.Raw, Credit: d.Credit, Used: d.Raw - d.Credit, ConcurrencyLimit: policy.ConcurrencyLimit, Active: s.groupActive[groupSlotKey(scope, group.ID)], Requests: d.Requests, Tokens: d.Tokens, Incomplete: d.Incomplete, AccountingComplete: d.Incomplete == 0 && view.Unclassified == 0, NextResetAt: nextGroupDay(now), ResetCutoff: d.ResetCutoff, Admission: "allowed"}
		if policy.DailyLimit != nil {
			remaining := max(USD(0), *policy.DailyLimit-item.Used)
			item.Remaining = &remaining
			item.Overage = max(USD(0), item.Used-*policy.DailyLimit)
		}
		switch {
		case !s.dirty.empty() || d.AccountingError != "":
			item.Admission = "accounting_unavailable"
		case !item.Enabled:
			item.Admission = "group_disabled"
		case item.DailyLimit != nil && item.Used >= *item.DailyLimit:
			item.Admission = "group_quota_exceeded"
		case item.ConcurrencyLimit != nil && item.Active >= *item.ConcurrencyLimit:
			item.Admission = "group_concurrency_exceeded"
		}
		view.Groups = append(view.Groups, item)
	}
	sort.Slice(view.Groups, func(i, j int) bool { return view.Groups[i].GroupID < view.Groups[j].GroupID })
	return view
}
