-- Group billing v15 schema. Execute only through the transactional migration.
-- *_at_ns / *_from_ns / *_to_ns: UTC Unix nanoseconds (same as existing DB).
-- Money: signed int64 nano-USD, checked in Go before every addition/conversion.
-- Model keys: canonicalized by the SAME resolver at admission and settlement.

CREATE TABLE gb_groups (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
  description TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'archived')),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  pool_json TEXT NOT NULL DEFAULT '{"mode":"inherit","allow_refs":[],"deny_refs":[],"allow_classes":[],"deny_classes":[]}' CHECK (json_valid(pool_json)),
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at_ns INTEGER NOT NULL,
  updated_at_ns INTEGER NOT NULL,
  published_at_ns INTEGER,
  CHECK ((status = 'draft' AND published_at_ns IS NULL) OR (status != 'draft' AND published_at_ns IS NOT NULL))
);

CREATE TABLE gb_group_models (
  group_id TEXT NOT NULL REFERENCES gb_groups(id) ON DELETE RESTRICT,
  model_key TEXT NOT NULL CHECK (length(model_key) > 0),
  display_model TEXT NOT NULL,
  PRIMARY KEY (group_id, model_key)
);

-- Unique ownership persists after archive: archiving is NOT a silent reassignment.
-- Publish inserts owners and changes group status in the same transaction.
CREATE TABLE gb_model_owners (
  model_key TEXT PRIMARY KEY,
  group_id TEXT NOT NULL,
  published_at_ns INTEGER NOT NULL,
  FOREIGN KEY (group_id, model_key) REFERENCES gb_group_models(group_id, model_key) ON DELETE RESTRICT
);

CREATE TRIGGER gb_models_no_insert_after_publish
BEFORE INSERT ON gb_group_models
WHEN (SELECT status FROM gb_groups WHERE id = NEW.group_id) != 'draft'
BEGIN SELECT RAISE(ABORT, 'published_model_membership_immutable'); END;

CREATE TRIGGER gb_models_no_update_after_publish
BEFORE UPDATE ON gb_group_models
WHEN (SELECT status FROM gb_groups WHERE id = OLD.group_id) != 'draft'
  OR (SELECT status FROM gb_groups WHERE id = NEW.group_id) != 'draft'
BEGIN SELECT RAISE(ABORT, 'published_model_membership_immutable'); END;

CREATE TRIGGER gb_models_no_delete_after_publish
BEFORE DELETE ON gb_group_models
WHEN (SELECT status FROM gb_groups WHERE id = OLD.group_id) != 'draft'
BEGIN SELECT RAISE(ABORT, 'published_model_membership_immutable'); END;

CREATE TRIGGER gb_groups_no_reopen
BEFORE UPDATE OF status ON gb_groups
WHEN (OLD.status = 'published' AND NEW.status = 'draft')
  OR (OLD.status = 'archived' AND NEW.status != 'archived')
BEGIN SELECT RAISE(ABORT, 'group_lifecycle_invalid'); END;

CREATE TABLE gb_plans (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at_ns INTEGER NOT NULL,
  updated_at_ns INTEGER NOT NULL
);

CREATE TABLE gb_plan_groups (
  plan_id TEXT NOT NULL REFERENCES gb_plans(id) ON DELETE RESTRICT,
  group_id TEXT NOT NULL REFERENCES gb_groups(id) ON DELETE RESTRICT,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  daily_limit_nano_usd INTEGER CHECK (daily_limit_nano_usd IS NULL OR (typeof(daily_limit_nano_usd) = 'integer' AND daily_limit_nano_usd > 0)),
  concurrency_limit INTEGER CHECK (concurrency_limit IS NULL OR (typeof(concurrency_limit) = 'integer' AND concurrency_limit BETWEEN 1 AND 10000)),
  PRIMARY KEY (plan_id, group_id)
);

-- One optimistic revision for all binding intervals of a Key.
CREATE TABLE gb_key_states (
  scope TEXT PRIMARY KEY REFERENCES api_keys(scope) ON DELETE RESTRICT,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  unclassified_count INTEGER NOT NULL DEFAULT 0 CHECK (unclassified_count >= 0)
);

-- Half-open intervals [effective_from_ns, effective_to_ns).
-- Service checks non-overlap in its serialized write transaction.
-- Before the first row the Key uses legacy mode. Do NOT infer old group usage.
CREATE TABLE gb_key_bindings (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  scope TEXT NOT NULL REFERENCES gb_key_states(scope) ON DELETE RESTRICT,
  mode TEXT NOT NULL CHECK (mode IN ('legacy', 'grouped')),
  plan_id TEXT REFERENCES gb_plans(id) ON DELETE RESTRICT,
  effective_from_ns INTEGER NOT NULL,
  effective_to_ns INTEGER,
  created_at_ns INTEGER NOT NULL,
  CHECK ((mode = 'legacy' AND plan_id IS NULL) OR (mode = 'grouped' AND plan_id IS NOT NULL)),
  CHECK (effective_to_ns IS NULL OR effective_to_ns > effective_from_ns),
  UNIQUE (scope, effective_from_ns)
);
CREATE INDEX gb_key_bindings_scope_time ON gb_key_bindings(scope, effective_from_ns, effective_to_ns);

-- No plan_id in the primary key: changing a plan cannot refill today's quota.
-- Date is computed by Go in Asia/Shanghai, never SQLite localtime.
-- All money writes reject integer overflow; SQLite must not promote to REAL.
CREATE TABLE gb_daily_usage (
  scope TEXT NOT NULL REFERENCES gb_key_states(scope) ON DELETE RESTRICT,
  group_id TEXT NOT NULL REFERENCES gb_groups(id) ON DELETE RESTRICT,
  business_date TEXT NOT NULL CHECK (length(business_date) = 10),
  raw_cost_nano_usd INTEGER NOT NULL DEFAULT 0 CHECK (typeof(raw_cost_nano_usd) = 'integer' AND raw_cost_nano_usd >= 0),
  reset_credit_nano_usd INTEGER NOT NULL DEFAULT 0 CHECK (typeof(reset_credit_nano_usd) = 'integer' AND reset_credit_nano_usd >= 0 AND reset_credit_nano_usd <= raw_cost_nano_usd),
  reset_cutoff_ns INTEGER,
  token_count INTEGER NOT NULL DEFAULT 0 CHECK (typeof(token_count) = 'integer' AND token_count >= 0),
  request_count INTEGER NOT NULL DEFAULT 0 CHECK (typeof(request_count) = 'integer' AND request_count >= 0),
  incomplete_count INTEGER NOT NULL DEFAULT 0 CHECK (incomplete_count >= 0),
  accounting_error TEXT NOT NULL DEFAULT '',
  updated_at_ns INTEGER NOT NULL,
  PRIMARY KEY (scope, group_id, business_date)
);

-- Effective spend = raw_cost - reset_credit.
-- Reset sets cutoff and credit=raw_cost atomically, without clearing events.
-- Late event RequestedAt < cutoff adds its cost to BOTH raw_cost and credit.
-- Repeated resets advance cutoff; original request/token counts stay intact.
-- raw_cost, credit, classification and the original event commit atomically.

CREATE TABLE gb_event_attributions (
  event_id INTEGER PRIMARY KEY REFERENCES request_events(id) ON DELETE CASCADE,
  group_id TEXT REFERENCES gb_groups(id) ON DELETE RESTRICT,
  classification TEXT NOT NULL CHECK (classification IN ('grouped', 'legacy', 'unclassified')),
  pricing_status TEXT NOT NULL CHECK (pricing_status IN ('priced', 'unpriced')),
  model_key TEXT NOT NULL DEFAULT '',
  mapping_published_at_ns INTEGER,
  business_date TEXT NOT NULL CHECK (length(business_date) = 10),
  cost_nano_usd INTEGER CHECK (cost_nano_usd IS NULL OR (typeof(cost_nano_usd) = 'integer' AND cost_nano_usd >= 0)),
  reason_code TEXT NOT NULL DEFAULT '',
  CHECK ((classification = 'grouped' AND group_id IS NOT NULL) OR (classification != 'grouped' AND group_id IS NULL)),
  CHECK ((pricing_status = 'priced' AND cost_nano_usd IS NOT NULL) OR (pricing_status = 'unpriced' AND cost_nano_usd IS NULL))
);
CREATE INDEX gb_events_group_date ON gb_event_attributions(group_id, business_date, event_id);

-- Active request slots are process memory, NOT persistent database rows.
-- Group access-block flags require a separate explicit recovery policy when a
-- database failure itself prevents persisting them; no crash recovery guarantee.

CREATE TABLE gb_shared_labels (
  keeper_instance_id TEXT NOT NULL,
  subject_kind TEXT NOT NULL CHECK (subject_kind IN ('downstream_key', 'upstream_identity')),
  subject_id TEXT NOT NULL,
  external_id INTEGER NOT NULL CHECK (external_id > 0),
  value TEXT NOT NULL DEFAULT '',
  source_note TEXT,
  fetched_at_ns INTEGER NOT NULL,
  PRIMARY KEY (keeper_instance_id, subject_kind, subject_id),
  UNIQUE (keeper_instance_id, subject_kind, external_id),
  CHECK (subject_kind != 'downstream_key' OR source_note IS NULL)
);
-- subject_id = existing caller scope for downstream; canonical serialized
-- credential fingerprint (resolved by exact auth_type + auth_index) for upstream.
-- A cache only. Never treat this table as a second authority for note writes.
-- No passwords, cookies or API key values in any new table or JSON column.

CREATE TABLE gb_audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at_ns INTEGER NOT NULL,
  actor_kind TEXT NOT NULL CHECK (actor_kind IN ('management-key', 'trusted-host-identity', 'system')),
  actor_id TEXT,
  action TEXT NOT NULL,
  object_type TEXT NOT NULL,
  object_id TEXT NOT NULL,
  before_json TEXT CHECK (before_json IS NULL OR json_valid(before_json)),
  after_json TEXT CHECK (after_json IS NULL OR json_valid(after_json)),
  reason TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL CHECK (result IN ('success', 'failure', 'remote_outcome_unknown')),
  CHECK (actor_kind = 'trusted-host-identity' OR actor_id IS NULL)
);
CREATE INDEX gb_audit_object_time ON gb_audit(object_type, object_id, at_ns, id);
-- Audit is append-only through application APIs. Sanitize external errors,
-- redact secrets in snapshots. Remote Keeper writes cannot share a SQL atomic
-- transaction: record intent/outcome honestly; a timeout is not proof of failure.
