package plugin

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"sync"
	"time"

	"cpa-key-billing/internal/billing"
	"cpa-key-billing/internal/keeper"
	"cpa-key-billing/internal/sqlite"
)

type App struct {
	keeperMu              sync.Mutex
	keeper                *keeper.Client
	keeperLast            time.Time
	keeperError           string
	store                 *billing.Store
	hostCaller            HostCaller
	admissionsMu          sync.Mutex
	admissions            map[string]*requestAdmission
	routingMu             sync.Mutex
	credentials           map[string]credentialView
	credentialsByRawID    map[string]string
	credentialRefsByIndex map[string]string
	scheduler             subsetScheduler
	pending               map[string]pendingRouteLog
	pendingSequence       uint64
}

func (a *App) SetHostCaller(caller HostCaller) {
	a.hostCaller = caller
}

func NewApp() *App {
	return newApp(billing.NewStore(openRepository, nil))
}

func newApp(store *billing.Store) *App {
	return &App{
		store:                 store,
		admissions:            make(map[string]*requestAdmission),
		credentials:           make(map[string]credentialView),
		credentialsByRawID:    make(map[string]string),
		credentialRefsByIndex: make(map[string]string),
		pending:               make(map[string]pendingRouteLog),
	}
}

func openRepository(path string) (billing.Repository, error) {
	return sqlite.Open(path)
}

// HandleMethod dispatches one host RPC call. A panic anywhere below is
// converted into an error envelope: the host fuses a panicking plugin, and
// taking the whole proxy down over a billing bug is not an acceptable trade.
func (a *App) HandleMethod(method string, request []byte) (response []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			response = nil
			err = fmt.Errorf("插件调用 %s 异常：%v", method, recovered)
			if a != nil && a.store != nil {
				a.store.AddPluginLog(billing.PluginLogError, "%v", err)
			}
		}
	}()
	return a.handleMethod(method, request)
}

func (a *App) handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case MethodPluginRegister, MethodPluginReconfigure:
		if errConfigure := a.configure(request); errConfigure != nil {
			a.store.AddPluginLog(billing.PluginLogError, "应用插件配置失败：%v", errConfigure)
			return nil, errConfigure
		}
		return OKEnvelope(registration())
	case MethodRequestInterceptBefore:
		return a.interceptBeforeAuth(request)
	case MethodRequestInterceptAfter:
		return a.interceptAfterAuth(request)
	case MethodRequestComplete:
		return a.completeRequest(request)
	case MethodSchedulerPick:
		return a.pickCredential(request)
	case MethodUsageHandle:
		return a.handleUsage(request)
	case MethodManagementRegister:
		return OKEnvelope(managementRegistration())
	case MethodManagementHandle:
		return a.handleManagement(request)
	default:
		return ErrorEnvelope("unknown_method", "不支持的插件方法："+method, http.StatusNotFound), nil
	}
}

func (a *App) Shutdown() {
	if a == nil || a.store == nil {
		return
	}
	a.store.Close()
}

func (a *App) configure(raw []byte) error {
	var req LifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return fmt.Errorf("解析插件生命周期请求：%w", errUnmarshal)
		}
	}
	cfg, errDecode := billing.DecodeConfig(req.ConfigYAML)
	if errDecode != nil {
		return errDecode
	}
	var keeperClient *keeper.Client
	if cfg.KeeperURL != "" {
		var err error
		keeperClient, err = keeper.New(cfg.KeeperURL, cfg.KeeperPasswordEnv)
		if err != nil {
			return err
		}
	}
	if errConfigure := func() error {
		a.routingMu.Lock()
		defer a.routingMu.Unlock()
		previous := a.store.ConfigCredentials()
		if err := a.store.Configure(cfg); err != nil {
			return err
		}
		if loaded := a.store.ConfigCredentials(); !maps.Equal(previous, loaded) {
			a.replaceSyncedCredentials(previous, loaded)
		}
		return nil
	}(); errConfigure != nil {
		return errConfigure
	}
	a.keeperMu.Lock()
	a.keeper = keeperClient
	a.keeperLast = time.Time{}
	a.keeperError = ""
	a.keeperMu.Unlock()
	// Refresh records its result; a download failure does not disable custom prices.
	_, _ = a.store.EnsureReferencePrices()
	return nil
}

func registration() Registration {
	return Registration{
		SchemaVersion: SchemaVersion,
		Metadata: Metadata{
			Name:             PluginName,
			Version:          Version,
			Author:           PluginName,
			GitHubRepository: GitHubRepository,
			ConfigFields: []ConfigField{
				{Name: "keeper_url", Type: "string", Description: "Keeper 服务地址（含可选子路径），留空保持本地备注"},
				{Name: "keeper_password_env", Type: "string", Description: "存放 Keeper 管理密码的环境变量名（不是密码本身）"},
				{
					Name:        "debug",
					Type:        "boolean",
					Description: "记录 debug 日志，包括路由日志和参考价匹配日志",
				},
				{
					Name:        "codex_fast_mode_billing",
					Type:        "boolean",
					Description: "请求 Codex 上游时指定 priority 档位，按 2.5 倍计费",
				},
				{
					Name:        "state_file",
					Type:        "string",
					Description: "计费数据库文件路径",
				},
			},
		},
		Capabilities: Capabilities{
			RequestInterceptor:     true,
			RequestLifecyclePlugin: true,
			UsagePlugin:            true,
			ManagementAPI:          true,
			Scheduler:              true,
		},
	}
}
