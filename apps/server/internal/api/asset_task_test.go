package api

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/db"
)

// TestDeleteUnuploadedAssets runs the cleanup inside a rolled-back
// transaction on the dev stack's test database (CONTRACT_SLOT picks the
// slot, as for the contract tests); it skips when that database is down.
func TestDeleteUnuploadedAssets(t *testing.T) {
	if got := (unuploadedAssetsJob{}).Kind(); got != "delete_unuploaded_file_asset" {
		t.Fatalf("kind %q", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	slot, _ := strconv.Atoi(os.Getenv("CONTRACT_SLOT"))
	url := os.Getenv("CONTRACT_DATABASE_URL")
	if url == "" {
		url = fmt.Sprintf("postgres://plane:plane@localhost:%d/plane_test", 55432+slot*10)
	}
	pool, err := db.Open(ctx, url, 2)
	if err != nil {
		t.Skipf("test database unavailable: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// key, age in days, uploaded, already deleted
	rows := []struct {
		key      string
		age      int
		uploaded bool
		deleted  bool
		want     bool // deleted afterwards
	}{
		{"cleanup-test/old-pending", 8, false, false, true},
		{"cleanup-test/old-uploaded", 8, true, false, false},
		{"cleanup-test/fresh-pending", 6, false, false, false},
		{"cleanup-test/old-deleted", 30, false, true, true},
	}
	for _, r := range rows {
		var deletedAt any
		if r.deleted {
			deletedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO file_assets (id, created_at, updated_at, attributes, asset, is_deleted,
				is_archived, size, is_uploaded, storage_metadata, deleted_at)
			VALUES (gen_random_uuid(), now() - make_interval(days => $2), now(), '{}', $1, false, false, 0, $3, '{}', $4)`,
			r.key, r.age, r.uploaded, deletedAt); err != nil {
			t.Fatal(err)
		}
	}
	n, err := deleteUnuploadedAssets(ctx, tx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Errorf("deleted %d rows, want at least the old pending one", n)
	}
	for _, r := range rows {
		var deletedAt *time.Time
		if err := tx.QueryRow(ctx, `SELECT deleted_at FROM file_assets WHERE asset = $1`, r.key).Scan(&deletedAt); err != nil {
			t.Fatal(err)
		}
		if (deletedAt != nil) != r.want {
			t.Errorf("%s: deleted_at %v, want deleted %v", r.key, deletedAt, r.want)
		}
		if r.deleted && deletedAt != nil && deletedAt.UTC().Year() != 2020 {
			t.Errorf("%s: an already deleted asset was touched", r.key)
		}
	}
	// A shorter window reaches the fresher one; is_deleted stays as it was
	// (queryset delete() only sets deleted_at).
	if _, err := deleteUnuploadedAssets(ctx, tx, 5); err != nil {
		t.Fatal(err)
	}
	var isDeleted bool
	var deletedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT is_deleted, deleted_at FROM file_assets WHERE asset = 'cleanup-test/fresh-pending'`).
		Scan(&isDeleted, &deletedAt)
	if err != nil && err != pgx.ErrNoRows {
		t.Fatal(err)
	}
	if deletedAt == nil || isDeleted {
		t.Errorf("fresh-pending after a 5-day window: is_deleted %v, deleted_at %v", isDeleted, deletedAt)
	}
}
