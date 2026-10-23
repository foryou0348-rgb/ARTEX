package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
)

func TestScopeContextSummaryBudgetAndPrompt(t *testing.T) {
	data := &db.TaskScopeContext{Total: 10000, CompanyCount: 10000, Revision: "test"}
	for i := 0; i < 100; i++ {
		data.Entries = append(data.Entries, db.ScopeContextEntry{Value: strings.Repeat("界", 2000), CompanyName: strings.Repeat("企", 2000), Source: "manual", Reason: strings.Repeat("长", 5000)})
		data.Companies = append(data.Companies, db.ScopeContextCompany{ID: int64(i + 1), Name: strings.Repeat("公", 2000)})
	}
	summary := scopeContextSummary(data)
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.Valid(raw) || len(raw) > 14000 || summary["has_more"] != true || summary["preview_truncated"] != true || summary["companies_has_more"] != true {
		t.Fatalf("unbounded/incomplete summary: bytes=%d", len(raw))
	}
	if len(summary["preview"].([]db.ScopeContextEntry)) != 8 || strings.Contains(string(raw), strings.Repeat("长", 10)) {
		t.Fatal("summary leaked full detail")
	}
	if len(data.Entries[0].Value) != len(strings.Repeat("界", 2000)) {
		t.Fatal("summary mutated original detail")
	}
	worker := renderWorkerGraphOverview(map[string]any{"coverage": map[string]any{"pct": 12}, "task_scope": summary})
	if !strings.Contains(worker, `"task_scope"`) || strings.Contains(worker, `"coverage"`) {
		t.Fatal("worker lost boundary context or retained coverage")
	}
	old := PromptOverride
	t.Cleanup(func() { PromptOverride = old })
	PromptOverride = func(string) (string, bool) { return "范围={{.Scope}}", true }
	if got := plannerSystem("goal", "/data", "/data", string(raw)); !strings.Contains(got, "范围="+string(raw)) || !strings.Contains(got, scopeContextGuidance) {
		t.Fatal("planner Scope template/tail missing")
	}
	tools := NewToolSet(nil, "")
	if tools.taskScopeSummary()["status"] != "unavailable" {
		t.Fatal("unavailable scope treated as empty")
	}
	for _, seed := range BuiltinToolSeeds() {
		if seed.Key == "get_task_scope" {
			if len(seed.Agents) != 3 {
				t.Fatalf("scope tool default bindings=%v", seed.Agents)
			}
			return
		}
	}
	t.Fatal("read-only scope tool not seeded")
}

func TestTaskScopeToolSurvivesDisabledCoverageAndChanges(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { _ = d.Close() })
	company, _, err := d.Companies().UpsertCompany("context-tool-"+t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Companies().DeleteCompany(company) })
	added, _, _, _ := d.Companies().AddScopeInputs(company, []db.ScopeInput{{Kind: "domain", Value: "context.invalid"}}, "test")
	if added != 1 {
		t.Fatal("seed company scope")
	}
	disabled := false
	task, err := d.CreateTaskWithOptions("context tool", "read only", db.TaskCreateOptions{CompanyIDs: []int64{company}, CoverageEnabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	tools := NewToolSet(d.Exploration(task.ExplorationID), "planner")
	tools.SetTaskID(task.ID)
	tools.SetAssetStore(d.Assets(), d.Companies())
	tools.SetCoverageEnabled(false)
	before := tools.graphOverviewData()["task_scope"].(map[string]any)
	if before["status"] != "ok" || before["company_reference_count"] != 1 {
		t.Fatalf("scope hidden with coverage disabled: %+v", before)
	}
	found := false
	for _, tool := range tools.DropCoverageTools(tools.PlannerTools()) {
		if tool.Name() == "get_task_scope" {
			found = true
			if !tool.IsReadOnly(nil) {
				t.Fatal("scope tool can write")
			}
		}
	}
	if !found {
		t.Fatal("scope lookup was removed with coverage tools")
	}
	tool := tools.getTaskScope()
	first := callReadJSON(t, tool, `{"page_size":1}`).(map[string]any)
	if first["has_more"] != true || len(first["entries"].([]any)) != 1 {
		t.Fatalf("pagination: %+v", first)
	}
	request, _ := json.Marshal(map[string]any{"offset": 1, "revision": first["revision"]})
	second := callReadJSON(t, tool, string(request)).(map[string]any)
	if !strings.Contains(fmt.Sprint(second), "context.invalid") {
		t.Fatal("full company rule unavailable")
	}
	if _, err := tools.ts.AddConstraint("deny", "do not touch other hosts", "human"); err != nil {
		t.Fatal(err)
	}
	changed := callReadJSON(t, tool, string(request)).(map[string]any)
	if changed["status"] != "changed" || changed["entries"] != nil {
		t.Fatalf("mixed old/new pages: %+v", changed)
	}
	after := tools.graphOverviewData()["task_scope"].(map[string]any)
	if after["revision"] == before["revision"] {
		t.Fatal("new round retained stale context")
	}
	for _, input := range []string{`{"offset":-1}`, `{"page_size":-1}`, `{"offset":1}`, `[]`} {
		result, err := tool.Call(context.Background(), json.RawMessage(input), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError {
			t.Fatalf("bad input accepted: %s", input)
		}
	}
}
