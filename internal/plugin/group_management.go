package plugin

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
)

const groupAPI = "/v1"

func init() {
	managementEndpoints = append(managementEndpoints, []managementEndpoint{
		{http.MethodGet, groupAPI + "/groups", "查看资源组", func(a *App, req ManagementRequest) ManagementResponse {
			items := a.store.ResourceGroups()
			if status := req.Query.Get("status"); status != "" {
				filtered := []billing.ResourceGroup{}
				for _, g := range items {
					if g.Status == status {
						filtered = append(filtered, g)
					}
				}
				items = filtered
			}
			return JSONResponse(200, map[string]any{"items": items})
		}},
		{http.MethodPost, groupAPI + "/groups", "新建资源组草稿", (*App).createResourceGroup},
		{http.MethodPatch, groupAPI + "/groups", "更新资源组", (*App).updateResourceGroup},
		{http.MethodPost, groupAPI + "/groups/publish", "发布资源组", (*App).publishResourceGroup},
		{http.MethodPost, groupAPI + "/groups/archive", "归档资源组", (*App).publishResourceGroup},
		{http.MethodPost, groupAPI + "/groups/preview", "静态检查资源组", (*App).previewResourceGroup},
		{http.MethodGet, groupAPI + "/plans", "查看分组计划", func(a *App, _ ManagementRequest) ManagementResponse {
			return JSONResponse(200, map[string]any{"items": a.store.GroupPlans()})
		}},
		{http.MethodPost, groupAPI + "/plans", "创建分组计划", (*App).saveGroupPlan},
		{http.MethodPut, groupAPI + "/plans", "更新分组计划", (*App).saveGroupPlan},
		{http.MethodGet, groupAPI + "/key-plan-bindings", "查看 Key 分组绑定", func(a *App, req ManagementRequest) ManagementResponse {
			return JSONResponse(200, a.store.GroupBinding(req.Query.Get("scope")))
		}},
		{http.MethodPut, groupAPI + "/key-plan-bindings", "安排 Key 分组绑定", (*App).bindGroupPlan},
		{http.MethodPost, groupAPI + "/key-plan-bindings/preview", "预览 Key 分组绑定", (*App).previewGroupBinding},
		{http.MethodGet, groupAPI + "/keys/group-usage", "查看 Key 分组用量", (*App).keyGroupUsage},
		{http.MethodPost, groupAPI + "/keys/group-reset", "重置单组日额度", (*App).resetGroupDay},
		{http.MethodGet, groupAPI + "/audit", "查看分组修改记录", (*App).groupAudit},
	}...)
	resourceEndpoints = append(resourceEndpoints, resourceEndpoint{groupAPI + "/subscription", func(a *App, req ManagementRequest, access viewAccess) ManagementResponse {
		if req.Query.Get("scope") != "" || req.Query.Get("api_key") != "" {
			return apiKeyJSONError(400, "invalid", "不能查询其他 Key")
		}
		if !access.Tracked {
			return apiKeyUnauthorized()
		}
		return apiKeyJSON(200, a.groupUsage(access.Scope))
	}})
}

type groupInput struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Models      []string          `json:"models"`
	Pool        billing.GroupPool `json:"pool"`
}

func (g groupInput) group() billing.ResourceGroup {
	return billing.ResourceGroup{Name: g.Name, Description: g.Description, Models: g.Models, Pool: g.Pool}
}
func groupAPIError(err error) ManagementResponse {
	response := errorResponse(err)
	for _, code := range []string{"revision_conflict", "model_group_conflict", "published_model_membership_immutable"} {
		if strings.HasPrefix(err.Error(), code+":") {
			return JSONError(response.StatusCode, code, strings.TrimSpace(strings.TrimPrefix(err.Error(), code+":")))
		}
	}
	if response.StatusCode == 500 {
		return JSONError(503, "storage_unavailable", "分组计费数据不可用，请检查服务端日志")
	}
	return response
}
func (a *App) createResourceGroup(req ManagementRequest) ManagementResponse {
	var body groupInput
	if err := decodeStrict(req.Body, &body); err != nil {
		return groupAPIError(err)
	}
	if r := a.validateNewCredentialRefs(append(append([]string{}, body.Pool.AllowRefs...), body.Pool.DenyRefs...), nil); r != nil {
		return *r
	}
	g, err := a.store.CreateResourceGroup(body.group())
	if err != nil {
		return groupAPIError(err)
	}
	return JSONResponse(200, g)
}
func (a *App) updateResourceGroup(req ManagementRequest) ManagementResponse {
	var body billing.GroupPatch
	if err := decodeStrict(req.Body, &body); err != nil {
		return groupAPIError(err)
	}
	if body.Pool != nil {
		old := []string{}
		for _, g := range a.store.ResourceGroups() {
			if g.ID == body.GroupID {
				old = append(g.Pool.AllowRefs, g.Pool.DenyRefs...)
			}
		}
		if r := a.validateNewCredentialRefs(append(append([]string{}, body.Pool.AllowRefs...), body.Pool.DenyRefs...), old); r != nil {
			return *r
		}
	}
	g, err := a.store.UpdateResourceGroup(body)
	if err != nil {
		return groupAPIError(err)
	}
	return JSONResponse(200, g)
}
func (a *App) publishResourceGroup(req ManagementRequest) ManagementResponse {
	var body struct {
		GroupID          string `json:"group_id"`
		ExpectedRevision int64  `json:"expected_revision"`
		Reason           string `json:"reason"`
	}
	if err := decodeStrict(req.Body, &body); err != nil {
		return groupAPIError(err)
	}
	g, err := a.store.PublishResourceGroup(body.GroupID, body.ExpectedRevision, body.Reason, strings.HasSuffix(req.Path, "/archive"))
	if err != nil {
		return groupAPIError(err)
	}
	return JSONResponse(200, g)
}
func (a *App) saveGroupPlan(req ManagementRequest) ManagementResponse {
	var body struct {
		PlanID           string                `json:"plan_id,omitempty"`
		ExpectedRevision int64                 `json:"expected_revision,omitempty"`
		Name             string                `json:"name"`
		Policies         []billing.GroupPolicy `json:"policies"`
		Reason           string                `json:"reason,omitempty"`
	}
	if err := decodeStrict(req.Body, &body); err != nil {
		return groupAPIError(err)
	}
	if req.Method == http.MethodPost && (body.PlanID != "" || body.ExpectedRevision != 0) {
		return JSONError(400, "invalid", "创建计划不能指定 ID 或修订号")
	}
	if req.Method == http.MethodPut && body.PlanID == "" {
		return JSONError(400, "invalid", "缺少 plan_id")
	}
	p, err := a.store.SaveGroupPlan(billing.GroupPlan{ID: body.PlanID, Name: body.Name, Policies: body.Policies}, body.ExpectedRevision, body.Reason)
	if err != nil {
		return groupAPIError(err)
	}
	return JSONResponse(200, p)
}

type groupBindingInput struct {
	Scope            string  `json:"scope"`
	Mode             string  `json:"mode"`
	PlanID           *string `json:"plan_id"`
	ExpectedRevision int64   `json:"expected_revision"`
	Effective        string  `json:"effective"`
	Reason           string  `json:"reason"`
}

func (a *App) bindGroupPlan(req ManagementRequest) ManagementResponse {
	var body groupBindingInput
	if err := decodeStrict(req.Body, &body); err != nil {
		return groupAPIError(err)
	}
	if body.Effective != "next_day" {
		return JSONError(400, "invalid", "首版只支持北京时间次日零点生效")
	}
	b, err := a.store.ScheduleGroupBinding(body.Scope, body.Mode, body.PlanID, body.ExpectedRevision, body.Reason)
	if err != nil {
		return groupAPIError(err)
	}
	return JSONResponse(200, b)
}
func (a *App) previewGroupBinding(req ManagementRequest) ManagementResponse {
	var body groupBindingInput
	if err := decodeStrict(req.Body, &body); err != nil {
		return groupAPIError(err)
	}
	key, ok := a.store.KeyViewForScope(body.Scope)
	if !ok {
		return JSONError(404, "not_found", "Key 不存在")
	}
	if body.Effective != "next_day" || (body.Mode != "legacy" && body.Mode != "grouped") || (body.Mode == "grouped" && body.PlanID == nil) || (body.Mode == "legacy" && body.PlanID != nil) {
		return JSONError(400, "invalid", "模式、计划或生效时间无效")
	}
	if body.PlanID != nil {
		found := false
		for _, p := range a.store.GroupPlans() {
			if p.ID == *body.PlanID {
				found = true
			}
		}
		if !found {
			return JSONError(404, "not_found", "分组计划不存在")
		}
	}
	if a.store.GroupBinding(body.Scope).Revision != body.ExpectedRevision {
		return JSONError(409, "revision_conflict", "绑定已变更，请刷新")
	}
	z, _ := time.LoadLocation("Asia/Shanghai")
	t := a.store.Now().In(z)
	boundary := time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, z).UTC()
	return JSONResponse(200, map[string]any{"binding": billing.GroupBindingPeriod{Mode: body.Mode, PlanID: body.PlanID, EffectiveFrom: boundary}, "replaced_legacy_constraints": []string{"旧套餐额度（" + key.PlanName + "）", "旧 Key 并发（" + strconv.Itoa(key.ConcurrencyLimit) + "）"}, "retained_routes": key.RouteBindings.RouteIDs, "warnings": []string{"次日生效；当前日账本和在途请求保留，原有模型/凭证权限继续生效"}})
}
func (a *App) keyGroupUsage(req ManagementRequest) ManagementResponse {
	scope := req.Query.Get("scope")
	if _, ok := a.store.KeyViewForScope(scope); !ok {
		return JSONError(404, "not_found", "Key 不存在")
	}
	return JSONResponse(200, a.groupUsage(scope))
}
func (a *App) resetGroupDay(req ManagementRequest) ManagementResponse {
	var body struct {
		Scope   string `json:"scope"`
		GroupID string `json:"group_id"`
		Reason  string `json:"reason"`
	}
	if err := decodeStrict(req.Body, &body); err != nil {
		return groupAPIError(err)
	}
	if err := a.store.ResetGroupDay(body.Scope, body.GroupID, body.Reason); err != nil {
		return groupAPIError(err)
	}
	for _, v := range a.store.GroupUsage(body.Scope).Groups {
		if v.GroupID == body.GroupID {
			return JSONResponse(200, v)
		}
	}
	return JSONError(404, "not_found", "Key 没有该组政策")
}
func (a *App) previewResourceGroup(req ManagementRequest) ManagementResponse {
	var body struct {
		Group groupInput `json:"group"`
		Scope string     `json:"scope,omitempty"`
	}
	if err := decodeStrict(req.Body, &body); err != nil {
		return groupAPIError(err)
	}
	if err := billing.ValidateResourceGroup(body.Group.group()); err != nil {
		return groupAPIError(err)
	}
	conflicts := []map[string]string{}
	for _, m := range body.Group.Models {
		for _, g := range a.store.ResourceGroups() {
			if g.Status == "draft" {
				continue
			}
			for _, owned := range g.Models {
				if strings.EqualFold(strings.TrimSpace(m), owned) {
					conflicts = append(conflicts, map[string]string{"model": m, "group_id": g.ID})
				}
			}
		}
	}
	if err := a.refreshCredentialInventory(); err != nil {
		return JSONError(503, "inventory_unavailable", "无法获取 CPA 凭证目录")
	}
	inventory := a.credentialInventory()
	missing := []string{}
	for _, ref := range append(append([]string{}, body.Group.Pool.AllowRefs...), body.Group.Pool.DenyRefs...) {
		found := false
		for _, v := range inventory {
			if v.Ref == ref {
				found = true
			}
		}
		if !found {
			missing = append(missing, ref)
		}
	}
	count := 0
	original := a.store.ResolveRouting(body.Scope, "", "")
	pool := billing.RoutingDecision{RouteRule: body.Group.Pool.Rule()}
	for _, c := range inventory {
		if !c.Disabled && !c.Unavailable && original.AllowsCredential(c.Ref, c.Source, c.Provider) && pool.AllowsCredential(c.Ref, c.Source, c.Provider) {
			count++
		}
	}
	return JSONResponse(200, map[string]any{"valid": len(conflicts) == 0 && len(missing) == 0 && len(body.Group.Models) > 0, "conflicts": conflicts, "unknown_models": []string{}, "missing_credentials": missing, "candidate_count": count, "dynamic_pool": len(body.Group.Pool.AllowClasses) > 0, "host_contract_verified": false, "warnings": []string{"模型是否存在需对照 CPA 模型目录；此检查不证明 usage Alias 契约，启用前须联调"}})
}
func (a *App) groupAudit(req ManagementRequest) ManagementResponse {
	limit := 50
	before := int64(0)
	var err error
	if raw := req.Query.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			return JSONError(400, "invalid", "limit 必须为 1–200")
		}
	}
	if raw := req.Query.Get("cursor"); raw != "" {
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before < 1 {
			return JSONError(400, "invalid", "cursor 无效")
		}
	}
	items, err := a.store.GroupAuditPage(before, limit+1, req.Query.Get("object_type"), req.Query.Get("object_id"))
	if err != nil {
		return groupAPIError(err)
	}
	var cursor *string
	if len(items) > limit {
		items = items[:limit]
		id := strconv.FormatInt(items[len(items)-1].ID, 10)
		cursor = &id
	}
	return JSONResponse(200, map[string]any{"items": items, "next_cursor": cursor})
}
