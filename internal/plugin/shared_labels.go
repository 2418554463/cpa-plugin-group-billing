package plugin

import (
	"net/http"
	"time"

	"cpa-key-billing/internal/billing"
	"cpa-key-billing/internal/keeper"
)

func init() {
	managementEndpoints = append(managementEndpoints,
		managementEndpoint{"GET", "/v1/shared-labels", "查看 Keeper 共享备注", (*App).sharedLabels},
		managementEndpoint{"PATCH", "/v1/shared-labels", "修改 Keeper 共享备注", (*App).patchSharedLabel},
		managementEndpoint{"GET", "/v1/integrations/keeper/status", "查看 Keeper 状态", (*App).keeperStatus},
		managementEndpoint{"POST", "/v1/integrations/keeper/refresh", "刷新 Keeper 备注", (*App).refreshKeeperAPI},
	)
}
func (a *App) keeperResolver() keeper.Resolver {
	a.routingMu.Lock()
	defer a.routingMu.Unlock()
	index := map[string]string{}
	for authIndex, ref := range a.credentialRefsByIndex {
		v, ok := a.credentials[ref]
		if !ok {
			continue
		}
		kind := "2/"
		if v.Source == billing.CredentialSourceAuthFiles {
			kind = "1/"
		}
		index[kind+authIndex] = ref
	}
	return func(authType int, authIndex string) (string, bool) {
		kind := ""
		if authType == 1 {
			kind = "1/"
		} else if authType == 2 {
			kind = "2/"
		} else {
			return "", false
		}
		ref, ok := index[kind+authIndex]
		return ref, ok
	}
}

// Refresh happens only inside management calls. No background polling or login
// is performed in interception, scheduling, usage, or ordinary user requests.
func (a *App) refreshKeeper(force bool) error {
	a.keeperMu.Lock()
	defer a.keeperMu.Unlock()
	if a.keeper == nil {
		return nil
	}
	if !force && time.Since(a.keeperLast) < 30*time.Second {
		return nil
	}
	a.keeperLast = time.Now()
	if err := a.refreshCredentialInventory(); err != nil {
		a.keeperError = "inventory_unavailable"
		return &keeper.Error{Code: a.keeperError, Status: 503}
	}
	labels, err := a.keeper.Snapshot(a.keeperResolver())
	if err == nil {
		err = a.store.CacheSharedLabels(a.keeper.Instance(), labels)
	}
	a.keeperError = ""
	if err != nil {
		_, a.keeperError = keeper.PublicError(err)
	}
	return err
}
func (a *App) keeperStatus(_ ManagementRequest) ManagementResponse {
	a.keeperMu.Lock()
	defer a.keeperMu.Unlock()
	value := map[string]any{"enabled": a.keeper != nil, "last_attempt": a.keeperLast, "error": a.keeperError, "stale": a.keeperError != "" || time.Since(a.keeperLast) > time.Minute, "write_consistency": "best_effort_expected_value"}
	if a.keeper != nil {
		value["instance_id"] = a.keeper.Instance()
		value["password_env_ready"] = a.keeper.Ready()
	}
	return JSONResponse(200, value)
}
func keeperAPIError(err error) ManagementResponse {
	status, code := keeper.PublicError(err)
	return JSONError(status, "keeper_"+code, "Keeper 备注未确认更新，请刷新后检查（"+code+"）")
}
func (a *App) refreshKeeperAPI(req ManagementRequest) ManagementResponse {
	if err := a.refreshKeeper(true); err != nil {
		return keeperAPIError(err)
	}
	return a.keeperStatus(req)
}
func (a *App) sharedLabels(req ManagementRequest) ManagementResponse {
	_ = a.refreshKeeper(false)
	a.keeperMu.Lock()
	defer a.keeperMu.Unlock()
	items := []billing.SharedLabel{}
	if a.keeper != nil {
		items = a.store.CachedSharedLabels(a.keeper.Instance())
	}
	return JSONResponse(200, map[string]any{"items": items, "enabled": a.keeper != nil, "stale": a.keeperError != "" || time.Since(a.keeperLast) > time.Minute, "error": a.keeperError})
}
func (a *App) sharedKeyLabel(scope, local string) string {
	a.keeperMu.Lock()
	defer a.keeperMu.Unlock()
	if a.keeper == nil {
		return local
	}
	for _, label := range a.store.CachedSharedLabels(a.keeper.Instance()) {
		if label.Kind == "downstream_key" && label.SubjectID == scope {
			return label.Value
		}
	}
	return "" // Configured Keeper is authoritative, including an empty alias.
}

func (a *App) groupUsage(scope string) billing.GroupKeyUsage {
	view := a.store.GroupUsage(scope)
	view.Label = a.sharedKeyLabel(scope, view.Label)
	a.keeperMu.Lock()
	view.LabelStale = a.keeper != nil && (a.keeperError != "" || time.Since(a.keeperLast) > time.Minute)
	a.keeperMu.Unlock()
	return view
}
func (a *App) updateSharedLabel(kind, subject, value string, expected *string) ManagementResponse {
	a.keeperMu.Lock()
	defer a.keeperMu.Unlock()
	if a.keeper == nil {
		return JSONError(409, "keeper_disabled", "尚未配置 Keeper")
	}
	if err := a.refreshCredentialInventory(); err != nil {
		return JSONError(503, "inventory_unavailable", "无法校验 CPA 凭证目录")
	}
	labels, err := a.keeper.Update(kind, subject, value, expected, a.keeperResolver())
	if err == nil {
		err = a.store.CacheSharedLabels(a.keeper.Instance(), labels)
		if err != nil {
			return JSONError(503, "keeper_write_outcome_unknown", "Keeper 已接受写入，但本地缓存失败；请刷新核对")
		}
	}
	if err != nil {
		_, a.keeperError = keeper.PublicError(err)
		return keeperAPIError(err)
	}
	a.keeperLast = time.Now()
	a.keeperError = ""
	return JSONResponse(200, map[string]any{"items": labels, "consistency": "best_effort_expected_value"})
}
func (a *App) patchSharedLabel(req ManagementRequest) ManagementResponse {
	var body struct {
		Kind     string  `json:"subject_kind"`
		Subject  string  `json:"subject_id"`
		Value    string  `json:"value"`
		Expected *string `json:"expected_value"`
	}
	if err := decodeStrict(req.Body, &body); err != nil {
		return errorResponse(err)
	}
	if body.Expected == nil {
		return JSONError(http.StatusBadRequest, "invalid", "必须提供 expected_value")
	}
	return a.updateSharedLabel(body.Kind, body.Subject, body.Value, body.Expected)
}
