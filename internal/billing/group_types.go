package billing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode"
)

var groupTimezone = func() *time.Location {
	z, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		panic(err)
	}
	return z
}()

// USD is an exact decimal at the API boundary and int64 nano-USD internally.
// A nil *USD is unlimited; zero is never an unlimited limit.
type USD int64

func ParseUSD(value string) (USD, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return 0, invalidf("金额必须为十进制字符串")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, invalidf("金额格式错误")
	}
	for _, p := range parts {
		if p == "" {
			return 0, invalidf("金额格式错误")
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return 0, invalidf("金额不能为负数或指数格式")
			}
		}
	}
	if len(parts[0]) > 1 && parts[0][0] == '0' {
		return 0, invalidf("金额格式错误")
	}
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	if len(frac) > 9 {
		return 0, invalidf("金额最多保留 9 位小数")
	}
	n, err := strconv.ParseInt(parts[0]+frac+strings.Repeat("0", 9-len(frac)), 10, 64)
	if err != nil {
		return 0, invalidf("金额超出 nano-USD 整数范围")
	}
	return USD(n), nil
}

func (v USD) String() string {
	whole, fraction := int64(v)/1_000_000_000, int64(v)%1_000_000_000
	if fraction == 0 {
		return strconv.FormatInt(whole, 10)
	}
	return fmt.Sprintf("%d.%s", whole, strings.TrimRight(fmt.Sprintf("%09d", fraction), "0"))
}
func (v USD) MarshalJSON() ([]byte, error) { return json.Marshal(v.String()) }
func (v *USD) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return invalidf("USD 必须使用字符串，不能使用 JSON 数字")
	}
	value, err := ParseUSD(text)
	if err == nil {
		*v = value
	}
	return err
}
func costUSD(value float64) (USD, bool) {
	if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	n := math.Round(value * 1_000_000_000)
	if n >= float64(math.MaxInt64) {
		return 0, false
	}
	return USD(n), true
}
func addGroupCount(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, false
	}
	return a + b, true
}

type GroupPool struct {
	Mode         string                       `json:"mode"`
	AllowRefs    []string                     `json:"allow_refs"`
	DenyRefs     []string                     `json:"deny_refs"`
	AllowClasses []CredentialProviderSelector `json:"allow_classes"`
	DenyClasses  []CredentialProviderSelector `json:"deny_classes"`
}

func (p GroupPool) Rule() RouteRule {
	return RouteRule{CredentialIDs: p.AllowRefs, DeniedCredentialIDs: p.DenyRefs, CredentialProviders: p.AllowClasses, DeniedCredentialProviders: p.DenyClasses}
}
func (p GroupPool) normalize() (GroupPool, error) {
	if p.Mode != "inherit" && p.Mode != "selected" {
		return p, invalidf("凭证池 mode 必须为 inherit 或 selected")
	}
	if p.Mode == "inherit" && len(p.AllowRefs)+len(p.AllowClasses) > 0 {
		return p, invalidf("inherit 不能包含允许列表")
	}
	if p.Mode == "selected" && len(p.AllowRefs)+len(p.AllowClasses) == 0 {
		return p, invalidf("指定凭证池不能为空")
	}
	rule, err := NormalizeRouteRule(p.Rule())
	if err != nil {
		return p, err
	}
	p.AllowRefs, p.DenyRefs, p.AllowClasses, p.DenyClasses = rule.CredentialIDs, rule.DeniedCredentialIDs, rule.CredentialProviders, rule.DeniedCredentialProviders
	if p.AllowRefs == nil {
		p.AllowRefs = []string{}
	}
	if p.DenyRefs == nil {
		p.DenyRefs = []string{}
	}
	if p.AllowClasses == nil {
		p.AllowClasses = []CredentialProviderSelector{}
	}
	if p.DenyClasses == nil {
		p.DenyClasses = []CredentialProviderSelector{}
	}
	return p, nil
}

type ResourceGroup struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	Enabled     bool       `json:"enabled"`
	Models      []string   `json:"models"`
	Pool        GroupPool  `json:"pool"`
	Revision    int64      `json:"revision"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	PublishedAt *time.Time `json:"published_at"`
}
type GroupPolicy struct {
	GroupID          string `json:"group_id"`
	Enabled          bool   `json:"enabled"`
	DailyLimit       *USD   `json:"daily_limit_usd"`
	ConcurrencyLimit *int   `json:"concurrency_limit"`
}
type GroupPlan struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Revision      int64         `json:"revision"`
	Policies      []GroupPolicy `json:"policies"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
	BoundKeyCount int           `json:"bound_key_count"`
}

func (p *GroupPolicy) UnmarshalJSON(raw []byte) error {
	type policy GroupPolicy
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, key := range []string{"group_id", "enabled", "daily_limit_usd", "concurrency_limit"} {
		if _, ok := fields[key]; !ok {
			return invalidf("政策缺少 %s；不限必须显式填写 null", key)
		}
	}
	if bytes.Equal(bytes.TrimSpace(fields["enabled"]), []byte("null")) {
		return invalidf("enabled 必须是布尔值")
	}
	var value policy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*p = GroupPolicy(value)
	return nil
}

type GroupBindingPeriod struct {
	Mode          string     `json:"mode"`
	PlanID        *string    `json:"plan_id"`
	EffectiveFrom time.Time  `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to"`
}
type GroupKeyBinding struct {
	Unclassified int64                `json:"unclassified_records"`
	Scope        string               `json:"scope"`
	Revision     int64                `json:"revision"`
	Periods      []GroupBindingPeriod `json:"periods"`
}
type GroupBindingView struct {
	Scope    string              `json:"scope"`
	Revision int64               `json:"revision"`
	Active   *GroupBindingPeriod `json:"active"`
	Pending  *GroupBindingPeriod `json:"pending"`
}
type GroupDay struct {
	AccountingError string
	Scope           string
	GroupID         string
	Date            string
	Raw             USD
	Credit          USD
	ResetCutoff     *time.Time
	Tokens          int64
	Requests        int64
	Incomplete      int64
	UpdatedAt       time.Time
}

func (d GroupDay) Key() string { return d.Scope + "\x00" + d.GroupID + "\x00" + d.Date }

type GroupAttribution struct {
	GroupID        string     `json:"group_id,omitempty"`
	Classification string     `json:"classification"`
	PricingStatus  string     `json:"pricing_status"`
	ModelKey       string     `json:"model_key"`
	PublishedAt    *time.Time `json:"mapping_published_at"`
	Date           string     `json:"business_date"`
	Cost           *USD       `json:"cost_usd"`
	Reason         string     `json:"reason_code,omitempty"`
}
type GroupAudit struct {
	ID         int64           `json:"id,string"`
	At         time.Time       `json:"at"`
	ActorKind  string          `json:"actor_kind"`
	ActorID    *string         `json:"actor_id"`
	Action     string          `json:"action"`
	ObjectType string          `json:"object_type"`
	ObjectID   string          `json:"object_id"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
	Reason     string          `json:"reason"`
	Result     string          `json:"result"`
}
type SharedLabel struct {
	InstanceID string    `json:"instance_id"`
	Kind       string    `json:"subject_kind"`
	SubjectID  string    `json:"subject_id"`
	ExternalID int64     `json:"external_id,string"`
	Value      string    `json:"value"`
	SourceNote *string   `json:"source_note"`
	FetchedAt  time.Time `json:"fetched_at"`
}

func (l SharedLabel) Key() string { return l.InstanceID + "\x00" + l.Kind + "\x00" + l.SubjectID }

// Configuration, historical binding boundaries and persistent day ledgers.
// No model/provider names are built in; clean installs start with empty maps.
type GroupState struct {
	Groups   map[string]ResourceGroup
	Plans    map[string]GroupPlan
	Bindings map[string]GroupKeyBinding
	Days     map[string]GroupDay
	Labels   map[string]SharedLabel
}

func NewGroupState() GroupState {
	return GroupState{Groups: map[string]ResourceGroup{}, Plans: map[string]GroupPlan{}, Bindings: map[string]GroupKeyBinding{}, Days: map[string]GroupDay{}, Labels: map[string]SharedLabel{}}
}
func (g GroupState) clone() GroupState {
	n := NewGroupState()
	for k, v := range g.Groups {
		raw, _ := json.Marshal(v)
		var copy ResourceGroup
		_ = json.Unmarshal(raw, &copy)
		n.Groups[k] = copy
	}
	for k, v := range g.Plans {
		raw, _ := json.Marshal(v)
		var copy GroupPlan
		_ = json.Unmarshal(raw, &copy)
		n.Plans[k] = copy
	}
	for k, v := range g.Bindings {
		raw, _ := json.Marshal(v)
		var copy GroupKeyBinding
		_ = json.Unmarshal(raw, &copy)
		n.Bindings[k] = copy
	}
	if g.Days != nil {
		n.Days = maps.Clone(g.Days)
	}
	if g.Labels != nil {
		n.Labels = maps.Clone(g.Labels)
	}
	return n
}
func groupDate(at time.Time) string { return at.In(groupTimezone).Format("2006-01-02") }
func nextGroupDay(at time.Time) time.Time {
	t := at.In(groupTimezone)
	return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, groupTimezone).UTC()
}
func groupModel(model string) string { return strings.ToLower(strings.TrimSpace(model)) }
func validGroupText(value string, maxRunes int, empty bool) bool {
	if (!empty && strings.TrimSpace(value) == "") || len([]rune(value)) > maxRunes {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func (g GroupState) binding(scope string, at time.Time) (GroupBindingPeriod, bool) {
	for _, p := range g.Bindings[scope].Periods {
		if !at.Before(p.EffectiveFrom) && (p.EffectiveTo == nil || at.Before(*p.EffectiveTo)) {
			return p, p.Mode == "grouped"
		}
	}
	return GroupBindingPeriod{}, false
}
func (g GroupState) modelOwner(model string, at time.Time) (ResourceGroup, bool) {
	key := groupModel(model)
	if key == "" {
		return ResourceGroup{}, false
	}
	for _, group := range g.Groups {
		if group.Status == "draft" || group.PublishedAt == nil || at.Before(*group.PublishedAt) {
			continue
		}
		for _, m := range group.Models {
			if groupModel(m) == key {
				return group, true
			}
		}
	}
	return ResourceGroup{}, false
}

// CPA thinking options are not model groups. Prefer an explicitly configured
// exact ID; otherwise remove only the host's trailing request-option suffix.
func (g GroupState) requestModelOwner(model string, at time.Time) (ResourceGroup, bool) {
	if owner, ok := g.modelOwner(model, at); ok {
		return owner, true
	}
	return g.modelOwner(ModelWithoutThinkingSuffix(model), at)
}
func (g GroupState) policy(planID, groupID string) (GroupPolicy, bool) {
	for _, p := range g.Plans[planID].Policies {
		if p.GroupID == groupID {
			return p, true
		}
	}
	return GroupPolicy{}, false
}
