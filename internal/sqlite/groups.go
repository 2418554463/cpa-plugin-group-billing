package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
)

func (d *DB) groupRows(query string, read func(*sql.Rows) error) error {
	rows, err := d.db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := read(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
func nullableTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	at := timeAt(value.Int64)
	return &at
}
func storedTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return nanos(*value)
}
func nullableUSD(value sql.NullInt64) *billing.USD {
	if !value.Valid {
		return nil
	}
	v := billing.USD(value.Int64)
	return &v
}
func nullableConcurrency(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	v := int(value.Int64)
	return &v
}
func storedUSD(value *billing.USD) any {
	if value == nil {
		return nil
	}
	return int64(*value)
}
func storedInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func (d *DB) loadGroupedState(state *billing.State) error {
	g := billing.NewGroupState()
	steps := []struct {
		query string
		read  func(*sql.Rows) error
	}{
		{`SELECT id,name,description,status,enabled,pool_json,revision,created_at_ns,updated_at_ns,published_at_ns FROM gb_groups`, func(r *sql.Rows) error {
			var v billing.ResourceGroup
			var pool string
			var created, updated int64
			var published sql.NullInt64
			if err := r.Scan(&v.ID, &v.Name, &v.Description, &v.Status, &v.Enabled, &pool, &v.Revision, &created, &updated, &published); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(pool), &v.Pool); err != nil {
				return err
			}
			v.CreatedAt = timeAt(created)
			v.UpdatedAt = timeAt(updated)
			v.PublishedAt = nullableTime(published)
			v.Models = []string{}
			g.Groups[v.ID] = v
			return nil
		}},
		{`SELECT group_id,display_model FROM gb_group_models ORDER BY group_id,model_key`, func(r *sql.Rows) error {
			var id, model string
			if err := r.Scan(&id, &model); err != nil {
				return err
			}
			v := g.Groups[id]
			v.Models = append(v.Models, model)
			g.Groups[id] = v
			return nil
		}},
		{`SELECT id,name,revision,created_at_ns,updated_at_ns FROM gb_plans`, func(r *sql.Rows) error {
			var v billing.GroupPlan
			var created, updated int64
			if err := r.Scan(&v.ID, &v.Name, &v.Revision, &created, &updated); err != nil {
				return err
			}
			v.CreatedAt = timeAt(created)
			v.UpdatedAt = timeAt(updated)
			v.Policies = []billing.GroupPolicy{}
			g.Plans[v.ID] = v
			return nil
		}},
		{`SELECT plan_id,group_id,enabled,daily_limit_nano_usd,concurrency_limit FROM gb_plan_groups ORDER BY plan_id,group_id`, func(r *sql.Rows) error {
			var id string
			var p billing.GroupPolicy
			var amount, limit sql.NullInt64
			if err := r.Scan(&id, &p.GroupID, &p.Enabled, &amount, &limit); err != nil {
				return err
			}
			p.DailyLimit = nullableUSD(amount)
			p.ConcurrencyLimit = nullableConcurrency(limit)
			v := g.Plans[id]
			v.Policies = append(v.Policies, p)
			g.Plans[id] = v
			return nil
		}},
		{`SELECT scope,revision,unclassified_count FROM gb_key_states`, func(r *sql.Rows) error {
			var v billing.GroupKeyBinding
			if err := r.Scan(&v.Scope, &v.Revision, &v.Unclassified); err != nil {
				return err
			}
			g.Bindings[v.Scope] = v
			return nil
		}},
		{`SELECT scope,mode,plan_id,effective_from_ns,effective_to_ns FROM gb_key_bindings ORDER BY scope,effective_from_ns`, func(r *sql.Rows) error {
			var scope string
			var p billing.GroupBindingPeriod
			var plan sql.NullString
			var from int64
			var to sql.NullInt64
			if err := r.Scan(&scope, &p.Mode, &plan, &from, &to); err != nil {
				return err
			}
			if plan.Valid {
				p.PlanID = &plan.String
			}
			p.EffectiveFrom = timeAt(from)
			p.EffectiveTo = nullableTime(to)
			v := g.Bindings[scope]
			v.Periods = append(v.Periods, p)
			g.Bindings[scope] = v
			return nil
		}},
		{`SELECT scope,group_id,business_date,raw_cost_nano_usd,reset_credit_nano_usd,reset_cutoff_ns,token_count,request_count,incomplete_count,accounting_error,updated_at_ns FROM gb_daily_usage`, func(r *sql.Rows) error {
			var v billing.GroupDay
			var raw, credit, updated int64
			var cutoff sql.NullInt64
			if err := r.Scan(&v.Scope, &v.GroupID, &v.Date, &raw, &credit, &cutoff, &v.Tokens, &v.Requests, &v.Incomplete, &v.AccountingError, &updated); err != nil {
				return err
			}
			v.Raw = billing.USD(raw)
			v.Credit = billing.USD(credit)
			v.ResetCutoff = nullableTime(cutoff)
			v.UpdatedAt = timeAt(updated)
			g.Days[v.Key()] = v
			return nil
		}},
		{`SELECT keeper_instance_id,subject_kind,subject_id,external_id,value,source_note,fetched_at_ns FROM gb_shared_labels`, func(r *sql.Rows) error {
			var v billing.SharedLabel
			var note sql.NullString
			var at int64
			if err := r.Scan(&v.InstanceID, &v.Kind, &v.SubjectID, &v.ExternalID, &v.Value, &note, &at); err != nil {
				return err
			}
			if note.Valid {
				v.SourceNote = &note.String
			}
			v.FetchedAt = timeAt(at)
			g.Labels[v.Key()] = v
			return nil
		}},
	}
	for _, step := range steps {
		if err := d.groupRows(step.query, step.read); err != nil {
			return fmt.Errorf("读取分组计费数据：%w", err)
		}
	}
	state.Grouped = g
	return nil
}

func saveGroupedState(tx *sql.Tx, state *billing.State, changes billing.Changes) error {
	if changes.GroupedConfig {
		if err := saveGroupedConfig(tx, state); err != nil {
			return err
		}
	}
	for _, key := range changes.GroupDays {
		v, ok := state.Grouped.Days[key]
		if !ok {
			return fmt.Errorf("分组日账本缺失")
		}
		_, err := tx.Exec(`INSERT INTO gb_daily_usage(scope,group_id,business_date,raw_cost_nano_usd,reset_credit_nano_usd,reset_cutoff_ns,token_count,request_count,incomplete_count,accounting_error,updated_at_ns)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(scope,group_id,business_date) DO UPDATE SET raw_cost_nano_usd=excluded.raw_cost_nano_usd,reset_credit_nano_usd=excluded.reset_credit_nano_usd,reset_cutoff_ns=excluded.reset_cutoff_ns,token_count=excluded.token_count,request_count=excluded.request_count,incomplete_count=excluded.incomplete_count,accounting_error=excluded.accounting_error,updated_at_ns=excluded.updated_at_ns`, v.Scope, v.GroupID, v.Date, int64(v.Raw), int64(v.Credit), storedTime(v.ResetCutoff), v.Tokens, v.Requests, v.Incomplete, v.AccountingError, nanos(v.UpdatedAt))
		if err != nil {
			return fmt.Errorf("保存分组日账本：%w", err)
		}
	}
	if changes.GroupLabels {
		if _, err := tx.Exec(`DELETE FROM gb_shared_labels`); err != nil {
			return err
		}
		for _, v := range state.Grouped.Labels {
			if _, err := tx.Exec(`INSERT INTO gb_shared_labels(keeper_instance_id,subject_kind,subject_id,external_id,value,source_note,fetched_at_ns) VALUES(?,?,?,?,?,NULLIF(?,''),?)`, v.InstanceID, v.Kind, v.SubjectID, v.ExternalID, v.Value, v.SourceNote, nanos(v.FetchedAt)); err != nil {
				return err
			}
		}
	}
	for _, v := range changes.GroupAudits {
		if _, err := tx.Exec(`INSERT INTO gb_audit(at_ns,actor_kind,actor_id,action,object_type,object_id,before_json,after_json,reason,result) VALUES(?,?,?,?,?,?,?,?,?,?)`, nanos(v.At), v.ActorKind, v.ActorID, v.Action, v.ObjectType, v.ObjectID, string(v.Before), string(v.After), v.Reason, v.Result); err != nil {
			return err
		}
	}
	return nil
}
func saveGroupedConfig(tx *sql.Tx, state *billing.State) error {
	ids := []string{}
	for id := range state.Grouped.Groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		g := state.Grouped.Groups[id]
		pool, err := json.Marshal(g.Pool)
		if err != nil {
			return err
		}
		// Insert a draft first. Owners and published status are committed together.
		_, err = tx.Exec(`INSERT INTO gb_groups(id,name,description,status,enabled,pool_json,revision,created_at_ns,updated_at_ns) VALUES(?,?,?,'draft',?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description,enabled=excluded.enabled,pool_json=excluded.pool_json,revision=excluded.revision,updated_at_ns=excluded.updated_at_ns`, g.ID, g.Name, g.Description, g.Enabled, string(pool), g.Revision, nanos(g.CreatedAt), nanos(g.UpdatedAt))
		if err != nil {
			return err
		}
		var status string
		if err := tx.QueryRow(`SELECT status FROM gb_groups WHERE id=?`, g.ID).Scan(&status); err != nil {
			return err
		}
		if status == "draft" {
			if _, err := tx.Exec(`DELETE FROM gb_group_models WHERE group_id=?`, g.ID); err != nil {
				return err
			}
			for _, m := range g.Models {
				if _, err := tx.Exec(`INSERT INTO gb_group_models(group_id,model_key,display_model) VALUES(?,?,?)`, g.ID, strings.ToLower(strings.TrimSpace(m)), m); err != nil {
					return err
				}
			}
		}
		if g.Status != "draft" {
			for _, m := range g.Models {
				key := strings.ToLower(strings.TrimSpace(m))
				if _, err := tx.Exec(`INSERT INTO gb_model_owners(model_key,group_id,published_at_ns) SELECT ?,?,? WHERE NOT EXISTS(SELECT 1 FROM gb_model_owners WHERE model_key=? AND group_id=?)`, key, g.ID, storedTime(g.PublishedAt), key, g.ID); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(`UPDATE gb_groups SET status=?,published_at_ns=? WHERE id=?`, g.Status, storedTime(g.PublishedAt), g.ID); err != nil {
			return err
		}
	}
	for _, p := range state.Grouped.Plans {
		if _, err := tx.Exec(`INSERT INTO gb_plans(id,name,revision,created_at_ns,updated_at_ns) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,revision=excluded.revision,updated_at_ns=excluded.updated_at_ns`, p.ID, p.Name, p.Revision, nanos(p.CreatedAt), nanos(p.UpdatedAt)); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM gb_plan_groups WHERE plan_id=?`, p.ID); err != nil {
			return err
		}
		for _, v := range p.Policies {
			if _, err := tx.Exec(`INSERT INTO gb_plan_groups(plan_id,group_id,enabled,daily_limit_nano_usd,concurrency_limit) VALUES(?,?,?,?,?)`, p.ID, v.GroupID, v.Enabled, storedUSD(v.DailyLimit), storedInt(v.ConcurrencyLimit)); err != nil {
				return err
			}
		}
	}
	for _, b := range state.Grouped.Bindings {
		if _, err := tx.Exec(`INSERT INTO gb_key_states(scope,revision,unclassified_count) VALUES(?,?,?) ON CONFLICT(scope) DO UPDATE SET revision=excluded.revision,unclassified_count=excluded.unclassified_count`, b.Scope, b.Revision, b.Unclassified); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM gb_key_bindings WHERE scope=?`, b.Scope); err != nil {
			return err
		}
		for _, v := range b.Periods {
			if _, err := tx.Exec(`INSERT INTO gb_key_bindings(scope,mode,plan_id,effective_from_ns,effective_to_ns,created_at_ns) VALUES(?,?,?,?,?,?)`, b.Scope, v.Mode, v.PlanID, nanos(v.EffectiveFrom), storedTime(v.EffectiveTo), nanos(v.EffectiveFrom)); err != nil {
				return err
			}
		}
	}
	return nil
}

func appendGroupAttribution(tx *sql.Tx, id int64, attr *billing.GroupAttribution) error {
	if attr == nil {
		return nil
	}
	var groupID any
	if attr.GroupID != "" {
		groupID = attr.GroupID
	}
	_, err := tx.Exec(`INSERT INTO gb_event_attributions(event_id,group_id,classification,pricing_status,model_key,mapping_published_at_ns,business_date,cost_nano_usd,reason_code) VALUES(?,?,?,?,?,?,?,?,?)`, id, groupID, attr.Classification, attr.PricingStatus, attr.ModelKey, storedTime(attr.PublishedAt), attr.Date, storedUSD(attr.Cost), attr.Reason)
	return err
}

func (d *DB) GroupAuditPage(before int64, limit int, kind, id string) ([]billing.GroupAudit, error) {
	where := " WHERE 1=1"
	args := []any{}
	if before > 0 {
		where += " AND id<?"
		args = append(args, before)
	}
	if kind != "" {
		where += " AND object_type=?"
		args = append(args, kind)
	}
	if id != "" {
		where += " AND object_id=?"
		args = append(args, id)
	}
	args = append(args, limit)
	rows, err := d.db.Query(`SELECT id,at_ns,actor_kind,actor_id,action,object_type,object_id,before_json,after_json,reason,result FROM gb_audit`+where+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []billing.GroupAudit{}
	for rows.Next() {
		var v billing.GroupAudit
		var at int64
		var actor, before, after sql.NullString
		if err := rows.Scan(&v.ID, &at, &v.ActorKind, &actor, &v.Action, &v.ObjectType, &v.ObjectID, &before, &after, &v.Reason, &v.Result); err != nil {
			return nil, err
		}
		v.At = timeAt(at)
		if actor.Valid {
			v.ActorID = &actor.String
		}
		if before.Valid {
			v.Before = json.RawMessage(before.String)
		}
		if after.Valid {
			v.After = json.RawMessage(after.String)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
