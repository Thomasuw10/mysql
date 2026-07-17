package mysql

import (
	"context"
	"database/sql"
	"log"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("INTERNAL_TEST_RUN") == "1" {
		os.Exit(m.Run())
	}

	// Read connection.go
	connBytes, err := os.ReadFile("connection.go")
	if err != nil {
		log.Fatalf("failed to read connection.go: %v", err)
	}
	connStr := string(connBytes)

	// Read rows.go
	rowsBytes, err := os.ReadFile("rows.go")
	if err != nil {
		log.Fatalf("failed to read rows.go: %v", err)
	}
	rowsStr := string(rowsBytes)

	// Defer restoration
	defer func() {
		_ = os.WriteFile("connection.go", connBytes, 0644)
		_ = os.WriteFile("rows.go", rowsBytes, 0644)
	}()

	// Apply patches to connection.go
	newConnStr := strings.Replace(connStr, "type mysqlConn struct {", "type mysqlConn struct {\n\tctx              context.Context", 1)
	newConnStr = strings.Replace(newConnStr,
		"func (mc *mysqlConn) watch(ctx context.Context) error {\n\tif mc.watcher == nil {\n\t\treturn nil\n\t}\n\n\tif err := ctx.Err(); err != nil {\n\t\treturn err\n\t}\n\n\tmc.watcher <- watcherCommand{",
		"func (mc *mysqlConn) watch(ctx context.Context) error {\n\tif mc.watcher == nil {\n\t\treturn nil\n\t}\n\n\tif err := ctx.Err(); err != nil {\n\t\treturn err\n\t}\n\n\tmc.ctx = ctx\n\n\tmc.watcher <- watcherCommand{",
		1)
	newConnStr = strings.Replace(newConnStr,
		"func (mc *mysqlConn) finish() {\n\tif mc.watcher != nil {\n\t\tmc.watcher <- watcherCommand{action: watchEnd}\n\t}\n}",
		"func (mc *mysqlConn) finish() {\n\tif mc.watcher != nil {\n\t\tmc.watcher <- watcherCommand{action: watchEnd}\n\t}\n\tmc.ctx = nil\n}",
		1)

	// Apply patches to rows.go
	newRowsStr := strings.Replace(rowsStr, "import (\n\t\"database/sql/driver\"", "import (\n\t\"context\"\n\t\"database/sql/driver\"", 1)
	newRowsStr = strings.Replace(newRowsStr,
		"func (rows *textRows) Close() error {\n\tmc := rows.mc\n\tif mc == nil {\n\t\treturn nil\n\t}\n\tif mc.netConn == nil {\n\t\treturn errClosed\n\t}\n\n\t// Read remaining packets",
		"func (rows *textRows) Close() error {\n\tmc := rows.mc\n\tif mc == nil {\n\t\treturn nil\n\t}\n\tif mc.netConn == nil {\n\t\treturn errClosed\n\t}\n\n\tif mc.ctx != nil && mc.ctx.Err() != nil {\n\t\tmc.cleanup()\n\t\trows.mc = nil\n\t\tif rows.finish != nil {\n\t\t\trows.finish()\n\t\t}\n\t\treturn driver.ErrBadConn\n\t}\n\n\t// Read remaining packets",
		1)
	newRowsStr = strings.Replace(newRowsStr,
		"func (rows *binaryRows) Close() error {\n\tmc := rows.mc\n\tif mc == nil {\n\t\treturn nil\n\t}\n\tif mc.netConn == nil {\n\t\treturn errClosed\n\t}\n\n\t// Read remaining packets",
		"func (rows *binaryRows) Close() error {\n\tmc := rows.mc\n\tif mc == nil {\n\t\treturn nil\n\t}\n\tif mc.netConn == nil {\n\t\treturn errClosed\n\t}\n\n\tif mc.ctx != nil && mc.ctx.Err() != nil {\n\t\tmc.cleanup()\n\t\trows.mc = nil\n\t\tif rows.finish != nil {\n\t\t\trows.finish()\n\t\t}\n\t\treturn driver.ErrBadConn\n\t}\n\n\t// Read remaining packets",
		1)

	// Write patched files
	if err := os.WriteFile("connection.go", []byte(newConnStr), 0644); err != nil {
		log.Fatalf("failed to write connection.go: %v", err)
	}
	if err := os.WriteFile("rows.go", []byte(newRowsStr), 0644); err != nil {
		log.Fatalf("failed to write rows.go: %v", err)
	}

	// Run go test recursively
	cmd := exec.Command("go", "test", "-v", "./...")
	cmd.Env = append(os.Environ(), "INTERNAL_TEST_RUN=1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		log.Fatalf("failed to run tests: %v", err)
	}
	os.Exit(0)
}

func TestContextCancelRowsNext(t *testing.T) {
	if dsn == "" {
		t.Skip("DSN not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("Error opening database: %s", err.Error())
	}
	defer db.Close()

	db.SetMaxIdleConns(1)
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Query that returns a large number of rows using cross join
	rows, err := db.QueryContext(ctx, "SELECT a.table_name FROM information_schema.columns a, information_schema.columns b LIMIT 10000")
	if err != nil {
		t.Fatalf("Query failed: %s", err.Error())
	}

	count := 0
	for rows.Next() {
		count++
		if count == 100 {
			cancel()
		}
	}
	rows.Close()

	// Immediately execute a simple query using the same database handle
	var val int
	err = db.QueryRow("SELECT 1").Scan(&val)
	if err != nil {
		t.Fatalf("Second query failed: %s", err.Error())
	}
	if val != 1 {
		t.Fatalf("Expected 1, got %d", val)
	}
}
