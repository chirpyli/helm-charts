package main

import "testing"

// TestSortBranchesForDeletion 确保逐条清理 timeline 时子分支先于父分支。
func TestSortBranchesForDeletion(t *testing.T) {
	main := &Branch{BranchID: "br-main", Name: "main"}
	child := &Branch{BranchID: "br-child", Name: "child", ParentID: main.BranchID}
	grandchild := &Branch{BranchID: "br-grandchild", Name: "grandchild", ParentID: child.BranchID}

	// 故意打乱输入顺序，验证排序结果与输入无关。
	out := sortBranchesForDeletion([]*Branch{main, grandchild, child})
	want := []string{"br-grandchild", "br-child", "br-main"}
	if len(out) != len(want) {
		t.Fatalf("分支数量错误：%d", len(out))
	}
	for i, id := range want {
		if out[i].BranchID != id {
			t.Fatalf("第 %d 个分支应为 %s，实际 %s", i, id, out[i].BranchID)
		}
	}

	// 同级分支按 branch_id 稳定排序（便于响应可重复）。
	a := &Branch{BranchID: "br-a", ParentID: main.BranchID}
	b := &Branch{BranchID: "br-b", ParentID: main.BranchID}
	out = sortBranchesForDeletion([]*Branch{b, a, main})
	if out[0].BranchID != "br-a" || out[1].BranchID != "br-b" || out[2].BranchID != "br-main" {
		t.Fatalf("同级分支排序不稳定：%v %v %v", out[0].BranchID, out[1].BranchID, out[2].BranchID)
	}
}
