package main

import (
	"io/fs"
	"testing"
)

func TestMigrationVersion(t *testing.T) {
	cases := []struct {
		name string
		want int
		ok   bool
	}{
		{"0001_init.sql", 1, true},
		{"0012_add_column.sql", 12, true},
		{"bad.sql", 0, false},
		{"0_init.sql", 0, false},
	}
	for _, c := range cases {
		got, err := migrationVersion(c.name)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("%s: 期望版本 %d，实际 %d err=%v", c.name, c.want, got, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: 期望解析失败，却返回 %d", c.name, got)
		}
	}
}

// 内嵌迁移目录必须随二进制一起打包，且首个版本存在。
func TestEmbeddedMigrations(t *testing.T) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		t.Fatalf("读取内嵌迁移目录：%v", err)
	}
	if len(entries) == 0 {
		t.Fatal("内嵌迁移目录为空")
	}
	if _, err := fs.ReadFile(migrationFS, "migrations/0001_init.sql"); err != nil {
		t.Fatalf("0001_init.sql 未打包：%v", err)
	}
}

// advisory lock 并发互斥与 checksum 校验需要真实 PG，由 store_postgres_test.go 覆盖。
func TestMigrationLockKeyIsStable(t *testing.T) {
	if migrationLockKey <= 0 {
		t.Fatal("迁移 advisory lock 键必须为非零常量")
	}
}
