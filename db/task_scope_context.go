package db

import (
	"encoding/json"
	"fmt"
)

// ScopeContextEntry preserves the distinction between inventory membership and
// operator constraints. Inherited inventory is read-only context, not a grant.
type ScopeContextEntry struct {
	Category    string `json:"category"`
	ID          int64  `json:"id"`
	ScopeID     int64  `json:"scope_id,omitempty"`
	TaskID      int64  `json:"task_id"`
	Inherited   bool   `json:"inherited"`
	CompanyID   int64  `json:"company_id,omitempty"`
	CompanyName string `json:"company_name,omitempty"`
	Kind        string `json:"kind"`
	Value       string `json:"value"`
	Source      string `json:"source"`
	Reason      string `json:"reason,omitempty"`
}

type ScopeContextCompany struct {
	TaskID    int64  `json:"task_id"`
	Inherited bool   `json:"inherited"`
	ID        int64  `json:"id"`
	Name      string `json:"name"`
}

// TaskScopeContext is one statement-consistent, paginated view. Revision is a
// content fingerprint, not an authorization token. Counts include direct source
// inventory but only the CURRENT task's operation constraints.
type TaskScopeContext struct {
	Revision        string                `json:"revision"`
	Total           int                   `json:"total"`
	ScopeCount      int                   `json:"scope_count"`
	InheritedCount  int                   `json:"inherited_scope_count"`
	ConstraintCount int                   `json:"constraint_count"`
	CompanyCount    int                   `json:"company_reference_count"`
	Companies       []ScopeContextCompany `json:"companies"`
	Entries         []ScopeContextEntry   `json:"entries"`
}

// ReadTaskScopeContext bounds returned rows in PostgreSQL instead of loading all
// enterprise rules into the agent process. Exactly one SQL statement supplies
// the fingerprint, counts and page, so edits cannot mix metadata with old rows.
func (s *AssetStore) ReadTaskScopeContext(taskID int64, offset, limit int) (*TaskScopeContext, error) {
	if taskID <= 0 || offset < 0 || limit < 1 || limit > 20 {
		return nil, fmt.Errorf("invalid task scope context page")
	}
	var exists bool
	var raw []byte
	err := s.db.QueryRow(`WITH `+directTaskContextCTE+`,
context_entries AS (
  SELECT CASE WHEN ts.kind='company' THEN 2 WHEN ts.source='auto' THEN 5 ELSE 3 END AS rank,
         'scope'::text AS category, ts.id, ts.id AS scope_id, ts.task_id,
         ts.task_id<>$1 AS inherited, COALESCE(ts.company_id,0) AS company_id,
         COALESCE(c.name,'') AS company_name, ts.kind,
         COALESCE(NULLIF(ts.domain,''),ts.net::text,NULLIF(ts.value,''),c.name,'') AS value,
         ts.source, COALESCE(ts.reason,'') AS reason
  FROM task_scope ts JOIN context_tasks ctx ON ctx.task_id=ts.task_id
  LEFT JOIN companies c ON c.id=ts.company_id
  UNION ALL
  SELECT 4, 'company_rule', cs.id, ts.id, ts.task_id, ts.task_id<>$1,
         c.id, c.name, cs.kind,
         COALESCE(NULLIF(cs.raw,''),cs.domain,cs.net::text,cs.value,''),
         ts.source, COALESCE(cs.reason,'')
  FROM task_scope ts JOIN context_tasks ctx ON ctx.task_id=ts.task_id
  JOIN companies c ON c.id=ts.company_id
  JOIN company_scope cs ON cs.company_id=c.id
  WHERE ts.kind='company'
  UNION ALL
  SELECT CASE WHEN tc.kind='deny' THEN 0 ELSE 1 END, 'constraint', tc.id, 0::bigint,
         ctx.task_id, false, 0::bigint, '', tc.kind, tc.text, COALESCE(tc.origin,''), ''
  FROM task_constraints tc JOIN context_tasks ctx ON ctx.exploration_id=tc.exploration_id
  WHERE ctx.task_id=$1
),
company_refs AS (
  SELECT DISTINCT task_id, inherited, company_id AS id, company_name AS name
  FROM context_entries WHERE category='scope' AND company_id>0
),
page AS (
  SELECT * FROM context_entries
  ORDER BY (task_id<>$1), rank, task_id, scope_id, id LIMIT $2 OFFSET $3
)
SELECT EXISTS(SELECT 1 FROM context_tasks WHERE task_id=$1),
       jsonb_build_object(
         'revision', (SELECT md5(COALESCE(string_agg(md5(to_jsonb(e)::text), '' ORDER BY task_id,rank,scope_id,id),'') ||
           COALESCE((SELECT string_agg(task_id::text,',' ORDER BY task_id) FROM context_tasks),'')) FROM context_entries e),
         'total', (SELECT count(*) FROM context_entries),
         'scope_count', (SELECT count(*) FROM context_entries WHERE category='scope'),
         'inherited_scope_count', (SELECT count(*) FROM context_entries WHERE category='scope' AND inherited),
         'constraint_count', (SELECT count(*) FROM context_entries WHERE category='constraint'),
         'company_reference_count', (SELECT count(*) FROM company_refs),
         'companies', COALESCE((SELECT jsonb_agg(to_jsonb(c) ORDER BY (task_id<>$1),task_id,id)
           FROM (SELECT * FROM company_refs ORDER BY (task_id<>$1),task_id,id LIMIT 8) c),'[]'::jsonb),
         'entries', COALESCE((SELECT jsonb_agg(to_jsonb(p) ORDER BY (task_id<>$1),rank,task_id,scope_id,id) FROM page p),'[]'::jsonb)
       )`, taskID, limit, offset).Scan(&exists, &raw)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("task scope context unavailable: task %d does not exist", taskID)
	}
	var result TaskScopeContext
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
