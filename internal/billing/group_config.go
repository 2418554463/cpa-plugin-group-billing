package billing

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
)

type GroupPatch struct {
	GroupID          string     `json:"group_id"`
	ExpectedRevision int64      `json:"expected_revision"`
	Name             *string    `json:"name,omitempty"`
	Description      *string    `json:"description,omitempty"`
	Enabled          *bool      `json:"enabled,omitempty"`
	Models           *[]string  `json:"models,omitempty"`
	Pool             *GroupPool `json:"pool,omitempty"`
	Reason           string     `json:"reason"`
}

func normalizeGroup(group ResourceGroup) (ResourceGroup, error) {
	group.Name = strings.TrimSpace(group.Name)
	if !validGroupText(group.Name, 80, false) || !validGroupText(group.Description, 1000, true) {
		return group, invalidf("组名或描述格式错误")
	}
	models, err := normalizeRouteStrings(group.Models)
	if err != nil {
		return group, err
	}
	if len(models) == 0 || len(models) != len(group.Models) {
		return group, invalidf("至少选择一个模型，且组内不能重复")
	}
	group.Models = models
	group.Pool, err = group.Pool.normalize()
	return group, err
}
func groupAudit(at time.Time, action, kind, id, reason string, before, after any) GroupAudit {
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	return GroupAudit{At: at, ActorKind: "management-key", Action: action, ObjectType: kind, ObjectID: id, Before: a, After: b, Reason: reason, Result: "success"}
}
func groupRevision(actual, expected int64) error {
	if actual != expected {
		return conflictf("revision_conflict: 数据已修改，请刷新后重试")
	}
	return nil
}
func groupReason(reason string) error {
	if !validGroupText(reason, 500, false) {
		return invalidf("必须填写不超过 500 字的修改理由")
	}
	return nil
}

func (s *Store) ResourceGroups() []ResourceGroup {
	groups := []ResourceGroup{}
	s.read(func(state *State) {
		for _, g := range state.Grouped.clone().Groups {
			groups = append(groups, g)
		}
	})
	slices.SortFunc(groups, func(a, b ResourceGroup) int { return strings.Compare(a.ID, b.ID) })
	return groups
}
func (s *Store) CreateResourceGroup(input ResourceGroup) (ResourceGroup, error) {
	group, err := normalizeGroup(input)
	if err != nil {
		return ResourceGroup{}, err
	}
	return editConfiguration(s, func(state *State) (ResourceGroup, Changes, error) {
		group.ID = freeID(group.Name, "group", func(id string) bool { _, ok := state.Grouped.Groups[id]; return ok })
		group.Status = "draft"
		group.Enabled = true
		group.Revision = 1
		group.PublishedAt = nil
		group.CreatedAt = s.Now()
		group.UpdatedAt = group.CreatedAt
		state.Grouped.Groups[group.ID] = group
		return group, Changes{GroupedConfig: true, GroupAudits: []GroupAudit{groupAudit(s.Now(), "create", "group", group.ID, "", nil, group)}}, nil
	})
}
func (s *Store) UpdateResourceGroup(patch GroupPatch) (ResourceGroup, error) {
	if err := groupReason(patch.Reason); err != nil {
		return ResourceGroup{}, err
	}
	return editConfiguration(s, func(state *State) (ResourceGroup, Changes, error) {
		g, ok := state.Grouped.Groups[patch.GroupID]
		if !ok {
			return g, Changes{}, notFoundf("资源组不存在")
		}
		if err := groupRevision(g.Revision, patch.ExpectedRevision); err != nil {
			return g, Changes{}, err
		}
		old := g
		if patch.Models != nil {
			if g.Status != "draft" {
				return g, Changes{}, conflictf("published_model_membership_immutable: 发布后的模型成员需维护迁移")
			}
			g.Models = *patch.Models
		}
		if patch.Name != nil {
			g.Name = *patch.Name
		}
		if patch.Description != nil {
			g.Description = *patch.Description
		}
		if patch.Enabled != nil {
			g.Enabled = *patch.Enabled
		}
		if patch.Pool != nil {
			g.Pool = *patch.Pool
		}
		var err error
		g, err = normalizeGroup(g)
		if err != nil {
			return g, Changes{}, err
		}
		g.Revision++
		g.UpdatedAt = s.Now()
		state.Grouped.Groups[g.ID] = g
		return g, Changes{GroupedConfig: true, GroupAudits: []GroupAudit{groupAudit(s.Now(), "update", "group", g.ID, patch.Reason, old, g)}}, nil
	})
}
func (s *Store) PublishResourceGroup(id string, revision int64, reason string, archive bool) (ResourceGroup, error) {
	if err := groupReason(reason); err != nil {
		return ResourceGroup{}, err
	}
	return editConfiguration(s, func(state *State) (ResourceGroup, Changes, error) {
		g, ok := state.Grouped.Groups[id]
		if !ok {
			return g, Changes{}, notFoundf("资源组不存在")
		}
		if err := groupRevision(g.Revision, revision); err != nil {
			return g, Changes{}, err
		}
		old := g
		action := "publish"
		if archive {
			if g.Status != "published" {
				return g, Changes{}, conflictf("只能归档已发布的组")
			}
			g.Status = "archived"
			g.Enabled = false
			action = "archive"
		} else {
			if g.Status != "draft" {
				return g, Changes{}, conflictf("只能发布草稿")
			}
			for _, m := range g.Models {
				if owner, exists := state.Grouped.modelOwner(m, s.Now()); exists {
					return g, Changes{}, conflictf("model_group_conflict: 模型 %q 已属于 %q", m, owner.Name)
				}
			}
			now := s.Now()
			g.Status = "published"
			g.PublishedAt = &now
		}
		g.Revision++
		g.UpdatedAt = s.Now()
		state.Grouped.Groups[id] = g
		return g, Changes{GroupedConfig: true, GroupAudits: []GroupAudit{groupAudit(s.Now(), action, "group", id, reason, old, g)}}, nil
	})
}
func (s *Store) GroupPlans() []GroupPlan {
	result := []GroupPlan{}
	s.read(func(state *State) {
		for _, p := range state.Grouped.clone().Plans {
			p.BoundKeyCount = 0
			for scope := range state.Grouped.Bindings {
				b, ok := state.Grouped.binding(scope, s.Now())
				if ok && b.PlanID != nil && *b.PlanID == p.ID {
					p.BoundKeyCount++
				}
			}
			result = append(result, p)
		}
	})
	slices.SortFunc(result, func(a, b GroupPlan) int { return strings.Compare(a.ID, b.ID) })
	return result
}
func validateGroupPlan(state *State, plan GroupPlan) error {
	if !validGroupText(plan.Name, 80, false) || len(plan.Policies) == 0 {
		return invalidf("请输入计划名称及至少一组政策")
	}
	seen := map[string]bool{}
	for _, p := range plan.Policies {
		g, ok := state.Grouped.Groups[p.GroupID]
		if !ok || g.Status != "published" {
			return invalidf("组 %q 尚未发布或已归档", p.GroupID)
		}
		if seen[p.GroupID] {
			return invalidf("同一组不能重复配置")
		}
		seen[p.GroupID] = true
		if p.DailyLimit != nil && *p.DailyLimit <= 0 {
			return invalidf("日金额必须大于零；null 表示不限")
		}
		if p.ConcurrencyLimit != nil && (*p.ConcurrencyLimit < 1 || *p.ConcurrencyLimit > MaxConcurrencyLimit) {
			return invalidf("并发必须为 1–10000 或 null")
		}
	}
	return nil
}
func (s *Store) SaveGroupPlan(plan GroupPlan, expected int64, reason string) (GroupPlan, error) {
	if plan.ID != "" {
		if err := groupReason(reason); err != nil {
			return GroupPlan{}, err
		}
	}
	return editConfiguration(s, func(state *State) (GroupPlan, Changes, error) {
		plan.Name = strings.TrimSpace(plan.Name)
		if err := validateGroupPlan(state, plan); err != nil {
			return plan, Changes{}, err
		}
		var old any
		action := "create"
		if plan.ID == "" {
			plan.ID = freeID(plan.Name, "group-plan", func(id string) bool { _, ok := state.Grouped.Plans[id]; return ok })
			plan.Revision = 1
			plan.CreatedAt = s.Now()
		} else {
			previous, ok := state.Grouped.Plans[plan.ID]
			if !ok {
				return plan, Changes{}, notFoundf("分组计划不存在")
			}
			if err := groupRevision(previous.Revision, expected); err != nil {
				return plan, Changes{}, err
			}
			old = previous
			action = "update"
			plan.Revision = previous.Revision + 1
			plan.CreatedAt = previous.CreatedAt
		}
		plan.UpdatedAt = s.Now()
		plan.BoundKeyCount = 0
		state.Grouped.Plans[plan.ID] = plan
		return plan, Changes{GroupedConfig: true, GroupAudits: []GroupAudit{groupAudit(s.Now(), action, "group_plan", plan.ID, reason, old, plan)}}, nil
	})
}
func bindingView(binding GroupKeyBinding, now time.Time) GroupBindingView {
	view := GroupBindingView{Scope: binding.Scope, Revision: binding.Revision}
	for _, p := range binding.Periods {
		p := p
		if now.Before(p.EffectiveFrom) {
			view.Pending = &p
		} else if p.EffectiveTo == nil || now.Before(*p.EffectiveTo) {
			view.Active = &p
		}
	}
	return view
}
func (s *Store) GroupBinding(scope string) GroupBindingView {
	view := GroupBindingView{Scope: scope}
	s.read(func(state *State) {
		if b, ok := state.Grouped.clone().Bindings[scope]; ok {
			view = bindingView(b, s.Now())
		}
	})
	return view
}
func (s *Store) ScheduleGroupBinding(scope, mode string, planID *string, expected int64, reason string) (GroupBindingView, error) {
	if err := groupReason(reason); err != nil {
		return GroupBindingView{}, err
	}
	if (mode != "legacy" && mode != "grouped") || (mode == "legacy" && planID != nil) || (mode == "grouped" && planID == nil) {
		return GroupBindingView{}, invalidf("模式和 plan_id 不匹配")
	}
	return editConfiguration(s, func(state *State) (GroupBindingView, Changes, error) {
		if state.liveKey(scope) == nil {
			return GroupBindingView{}, Changes{}, notFoundf("API Key 不存在，请先同步")
		}
		if planID != nil {
			if _, ok := state.Grouped.Plans[*planID]; !ok {
				return GroupBindingView{}, Changes{}, notFoundf("分组计划不存在")
			}
		}
		b := state.Grouped.Bindings[scope]
		old := b
		if err := groupRevision(b.Revision, expected); err != nil {
			return GroupBindingView{}, Changes{}, err
		}
		now := s.Now()
		boundary := nextGroupDay(now)
		periods := []GroupBindingPeriod{}
		for _, p := range b.Periods {
			if p.EffectiveFrom.After(now) {
				continue
			}
			if p.EffectiveTo == nil || p.EffectiveTo.After(boundary) {
				end := boundary
				p.EffectiveTo = &end
			}
			periods = append(periods, p)
		}
		periods = append(periods, GroupBindingPeriod{Mode: mode, PlanID: planID, EffectiveFrom: boundary})
		b = GroupKeyBinding{Scope: scope, Revision: b.Revision + 1, Periods: periods, Unclassified: b.Unclassified}
		state.Grouped.Bindings[scope] = b
		return bindingView(b, now), Changes{GroupedConfig: true, GroupAudits: []GroupAudit{groupAudit(now, "schedule", "key_binding", scope, reason, old, b)}}, nil
	})
}
func (s *Store) IsGrouped(scope string, at time.Time) bool {
	var found bool
	s.read(func(state *State) { _, found = state.Grouped.binding(scope, at) })
	return found
}

func (s *Store) ResetGroupDay(scope, groupID, reason string) error {
	if err := groupReason(reason); err != nil {
		return err
	}
	_, err := editConfiguration(s, func(state *State) (struct{}, Changes, error) {
		if state.liveKey(scope) == nil {
			return struct{}{}, Changes{}, notFoundf("API Key 不存在")
		}
		if _, ok := state.Grouped.Groups[groupID]; !ok {
			return struct{}{}, Changes{}, notFoundf("资源组不存在")
		}
		now := s.Now()
		d := GroupDay{Scope: scope, GroupID: groupID, Date: groupDate(now)}
		key := d.Key()
		binding, grouped := state.Grouped.binding(scope, now)
		if !grouped {
			return struct{}{}, Changes{}, conflictf("Key 尚未处于分组模式")
		}
		if _, allowed := state.Grouped.policy(*binding.PlanID, groupID); !allowed {
			if _, consumed := state.Grouped.Days[key]; !consumed {
				return struct{}{}, Changes{}, notFoundf("Key 没有该组政策或用量")
			}
		}
		if old, ok := state.Grouped.Days[key]; ok {
			d = old
		}
		old := d
		d.Credit = d.Raw
		d.ResetCutoff = &now
		d.UpdatedAt = now
		state.Grouped.Days[key] = d
		return struct{}{}, Changes{GroupDays: []string{key}, GroupAudits: []GroupAudit{groupAudit(now, "reset", "group_day", scope+"/"+groupID+"/"+d.Date, reason, old, d)}}, nil
	})
	return err
}
func (s *Store) CachedSharedLabels(instance string) []SharedLabel {
	result := []SharedLabel{}
	s.read(func(state *State) {
		for _, v := range state.Grouped.Labels {
			if v.InstanceID == instance {
				result = append(result, v)
			}
		}
	})
	return result
}
func (s *Store) CacheSharedLabels(instance string, labels []SharedLabel) error {
	_, err := editConfiguration(s, func(state *State) (struct{}, Changes, error) {
		for key, label := range state.Grouped.Labels {
			if label.InstanceID == instance {
				delete(state.Grouped.Labels, key)
			}
		}
		for _, l := range labels {
			state.Grouped.Labels[l.Key()] = l
		}
		return struct{}{}, Changes{GroupLabels: true}, nil
	})
	return err
}

func (s *Store) GroupAuditPage(before int64, limit int, kind, id string) ([]GroupAudit, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if repo, ok := s.repo.(interface {
		GroupAuditPage(int64, int, string, string) ([]GroupAudit, error)
	}); ok {
		return repo.GroupAuditPage(before, limit, kind, id)
	}
	return []GroupAudit{}, nil
}

func ValidateResourceGroup(g ResourceGroup) error { _, err := normalizeGroup(g); return err }
