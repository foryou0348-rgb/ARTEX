package db

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func scopeContextTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func scopeContextTask(t *testing.T, d *DB, opts TaskCreateOptions) *Task {
	t.Helper()
	task, err := d.CreateTaskWithOptions("scope context test", "read only", opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	return task
}

func TestTaskScopeContextPaginationAndIsolation(t *testing.T) {
	d := scopeContextTestDB(t)
	cs, assets := d.Companies(), d.Assets()
	cid, _, err := cs.UpsertCompany(fmt.Sprintf("scope-context-%d", time.Now().UnixNano()), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.DeleteCompany(cid) })
	inputs := make([]ScopeInput, 64)
	for i := range inputs {
		inputs[i] = ScopeInput{Kind: "keyword", Value: fmt.Sprintf("范围规则 %d", i)}
	}
	added, _, invalid, errs := cs.AddScopeInputs(cid, inputs, "test")
	if added != 64 || invalid != 0 || len(errs) != 0 {
		t.Fatalf("seed rules: %d %d %v", added, invalid, errs)
	}
	grand := scopeContextTask(t, d, TaskCreateOptions{})
	source := scopeContextTask(t, d, TaskCreateOptions{SourceTaskIDs: []int64{grand.ID}})
	current := scopeContextTask(t, d, TaskCreateOptions{CompanyIDs: []int64{cid}, SourceTaskIDs: []int64{source.ID}})
	other := scopeContextTask(t, d, TaskCreateOptions{})
	for _, item := range []struct {
		id    int64
		value string
	}{{grand.ID, "grand.invalid"}, {source.ID, "source.invalid"}, {other.ID, "unrelated.invalid"}} {
		if _, err := assets.AddAgentScope(item.id, "subdomain", item.value, "test", "manual"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Exploration(source.ExplorationID).AddConstraint("allow", "source-only permission", "human"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"allow", "deny"} {
		if _, err := d.Exploration(current.ExplorationID).AddConstraint(kind, "current "+kind, "human"); err != nil {
			t.Fatal(err)
		}
	}
	first, err := assets.ReadTaskScopeContext(current.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 68 || first.ScopeCount != 2 || first.ConstraintCount != 2 || first.InheritedCount != 1 || len(first.Entries) != 20 || first.CompanyCount != 1 {
		t.Fatalf("unexpected context counts: %+v", first)
	}
	if len(first.Companies) != 1 || first.Companies[0].ID != cid || first.Companies[0].Inherited {
		t.Fatalf("company references: %+v", first.Companies)
	}
	if first.Entries[0].Category != "constraint" || first.Entries[0].Kind != "deny" {
		t.Fatalf("operator denials must appear first: %+v", first.Entries[0])
	}
	seen := map[string]bool{}
	for offset := 0; offset < first.Total; offset += 20 {
		page, err := assets.ReadTaskScopeContext(current.ID, offset, 20)
		if err != nil {
			t.Fatal(err)
		}
		if page.Revision != first.Revision {
			t.Fatal("stable context changed revision across pages")
		}
		for _, entry := range page.Entries {
			key := fmt.Sprintf("%s:%d:%d:%d", entry.Category, entry.TaskID, entry.ScopeID, entry.ID)
			if seen[key] {
				t.Fatalf("duplicate page entry %s", key)
			}
			seen[key] = true
			if entry.TaskID != current.ID && (entry.TaskID != source.ID || !entry.Inherited || entry.Category == "constraint") {
				t.Fatalf("out-of-context entry: %+v", entry)
			}
			if strings.Contains(entry.Value, "source-only") || strings.Contains(entry.Value, "grand.invalid") || strings.Contains(entry.Value, "unrelated.invalid") {
				t.Fatalf("leaked context: %+v", entry)
			}
		}
	}
	if len(seen) != first.Total {
		t.Fatalf("pagination lost rows: %d/%d", len(seen), first.Total)
	}
	if _, err := assets.ReadTaskScopeContext(current.ID, 0, 21); err == nil {
		t.Fatal("unbounded page accepted")
	}
	if _, err := assets.ReadTaskScopeContext(0, 0, 8); err == nil {
		t.Fatal("missing task accepted")
	}
}

func TestTaskScopeContextRevisionTracksContent(t *testing.T) {
	d := scopeContextTestDB(t)
	cs, assets := d.Companies(), d.Assets()
	cid, _, err := cs.UpsertCompany(fmt.Sprintf("scope-revision-%d", time.Now().UnixNano()), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.DeleteCompany(cid) })
	added, _, _, _ := cs.AddScopeInputs(cid, []ScopeInput{{Kind: "keyword", Value: "original keyword"}}, "test")
	if added != 1 {
		t.Fatal("seed company rule")
	}
	task := scopeContextTask(t, d, TaskCreateOptions{CompanyIDs: []int64{cid}})
	constraintID, err := d.Exploration(task.ExplorationID).AddConstraint("deny", "original constraint", "human")
	if err != nil {
		t.Fatal(err)
	}
	read := func() *TaskScopeContext {
		t.Helper()
		result, err := assets.ReadTaskScopeContext(task.ID, 0, 8)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	previous := read()
	if previous.Revision != read().Revision {
		t.Fatal("unchanged context is unstable")
	}
	mutations := []func() error{
		func() error {
			_, err := d.Exec("UPDATE company_scope SET raw='edited keyword', value='edited keyword' WHERE company_id=$1", cid)
			return err
		},
		func() error {
			return d.Exploration(task.ExplorationID).UpdateConstraint(constraintID, "deny", "edited constraint")
		},
		func() error {
			_, err := d.Exec("UPDATE companies SET name=name || '-renamed' WHERE id=$1", cid)
			return err
		},
		func() error { _, err := d.Exec("DELETE FROM company_scope WHERE company_id=$1", cid); return err },
	}
	for i, mutate := range mutations {
		if err := mutate(); err != nil {
			t.Fatal(err)
		}
		next := read()
		if next.Revision == previous.Revision {
			t.Fatalf("mutation %d did not invalidate context", i)
		}
		previous = next
	}
	if err := d.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := assets.ReadTaskScopeContext(task.ID, 0, 8); err == nil {
		t.Fatal("deleted task looks like empty scope")
	}
	softDeleted := scopeContextTask(t, d, TaskCreateOptions{})
	if _, err := d.Exec("UPDATE tasks SET deleted_at=now() WHERE id=$1", softDeleted.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := assets.ReadTaskScopeContext(softDeleted.ID, 0, 8); err == nil {
		t.Fatal("soft deleted task accepted")
	}
}
