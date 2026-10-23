package agent

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

const scopeContextGuidance = `

【任务范围上下文】：graph_overview.task_scope 是独立于 coverage 的只读范围摘要，每轮读取 revision；它变化时重新核对范围，不能沿用旧页。摘要有 preview_truncated / has_more 时，若需要完整边界，在工具可用时用 get_task_scope(offset=0, revision=摘要版本) 分页读取；不确定范围时先澄清，不猜测。category=scope/company_rule 是资产归属/查询范围，source=auto 是发现记录；inherited=true 是直接关联任务的只读背景。它们均不构成新增操作授权，也不能覆盖当前任务的 allow/deny 操作约束。Worker 仍只执行已领意图，不因范围摘要扩大任务。状态 unavailable 表示读取失败，不表示范围为空或允许任意目标。`

func scopePreviewText(value string, limit int) (string, bool) {
	if utf8.RuneCountInString(value) <= limit {
		return value, false
	}
	return string([]rune(value)[:limit]) + "…", true
}

// scopeContextSummary has a fixed row/text budget even for huge enterprise scopes.
func scopeContextSummary(data *db.TaskScopeContext) map[string]any {
	entries := data.Entries
	if len(entries) > 8 {
		entries = entries[:8]
	}
	preview := make([]db.ScopeContextEntry, 0, len(entries))
	truncated := false
	for _, row := range entries {
		var cut bool
		row.Value, cut = scopePreviewText(row.Value, 160)
		truncated = truncated || cut
		row.CompanyName, cut = scopePreviewText(row.CompanyName, 80)
		truncated = truncated || cut
		row.Source, cut = scopePreviewText(row.Source, 40)
		truncated = truncated || cut
		row.Reason = "" // full attribution belongs in the paginated detail
		preview = append(preview, row)
	}
	companyRows := data.Companies
	if len(companyRows) > 8 {
		companyRows = companyRows[:8]
	}
	companies := append([]db.ScopeContextCompany{}, companyRows...)
	for i := range companies {
		var cut bool
		companies[i].Name, cut = scopePreviewText(companies[i].Name, 80)
		truncated = truncated || cut
	}
	status := "ok"
	if data.Total == 0 {
		status = "empty"
	}
	return map[string]any{
		"status": status, "revision": data.Revision, "total": data.Total,
		"scope_count": data.ScopeCount, "inherited_scope_count": data.InheritedCount,
		"constraint_count": data.ConstraintCount, "companies": companies,
		"company_reference_count": data.CompanyCount, "companies_has_more": data.CompanyCount > len(companies),
		"preview": preview, "preview_truncated": truncated, "has_more": data.Total > len(preview),
		"detail_tool": "get_task_scope", "note": "只读上下文，不是操作授权；继承范围/自动发现不得扩大当前任务授权。操作以当前任务约束为准。",
	}
}

func (t *ToolSet) taskScopeSummary() map[string]any {
	if t.as == nil || t.taskID <= 0 {
		return map[string]any{"status": "unavailable", "reason": "任务上下文或资产库不可用；不能据此推定范围为空或允许任意目标"}
	}
	data, err := t.as.ReadTaskScopeContext(t.taskID, 0, 8)
	if err != nil {
		return map[string]any{"status": "unavailable", "reason": "任务范围读取失败；先核实边界，不使用旧范围或猜测"}
	}
	return scopeContextSummary(data)
}

// No task_id/company_id input: callers cannot turn this into a global company query.
func (t *ToolSet) getTaskScope() actool.CoreTool {
	return readTool("get_task_scope",
		"只读分页读取【当前任务及直接关联任务】的范围详情与当前任务的操作约束；独立于覆盖率开关。保留 task_id/inherited/source；继承或自动发现不是新增操作授权。返回 revision/total/entries/has_more/next_offset。分页时传同一 revision，变化则从第一页重新读取。",
		obj(map[string]any{
			"offset":    intp("从 0 开始的偏移，默认 0"),
			"page_size": intp("每页条数，默认 10，最大 20"),
			"revision":  str("可选：摘要或上一页的 revision；不一致则拒绝混合新旧页"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil || t.taskID <= 0 {
				return actool.Errorf("get_task_scope 需要当前任务上下文和资产库"), nil
			}
			var args struct {
				Offset   int    `json:"offset"`
				PageSize int    `json:"page_size"`
				Revision string `json:"revision"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return actool.Errorf("范围查询参数必须为 JSON 对象"), nil
			}
			if args.Offset < 0 || args.PageSize < 0 {
				return actool.Errorf("offset/page_size 不得为负数"), nil
			}
			if args.PageSize == 0 {
				args.PageSize = 10
			}
			if args.PageSize > 20 {
				args.PageSize = 20
			}
			data, err := t.as.ReadTaskScopeContext(t.taskID, args.Offset, args.PageSize)
			if err != nil {
				return actool.Errorf("范围读取失败，不能推定范围为空或允许任意目标"), nil
			}
			if args.Revision != "" && args.Revision != data.Revision {
				return jsonResult(map[string]any{"status": "changed", "revision": data.Revision, "restart_offset": 0, "note": "范围已变化，旧分页无效；从 offset=0 重新核对"})
			}
			next := args.Offset + len(data.Entries)
			hasMore := next < data.Total
			out := map[string]any{"status": "ok", "revision": data.Revision, "total": data.Total, "entries": data.Entries, "has_more": hasMore}
			if hasMore {
				out["next_offset"] = next
			}
			return jsonResult(out)
		})
}
