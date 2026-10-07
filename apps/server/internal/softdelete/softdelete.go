// Package softdelete ports Plane's soft deletion: SoftDeleteModel.delete()
// marks a row deleted and plane.bgtasks.deletion_task.soft_delete_related_objects
// then cascades deleted_at down every reverse foreign key (SET_NULL
// relations are nulled instead). Queryset .delete() calls never cascade and
// are plain UPDATEs in the handlers.
package softdelete

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/db"
)

type relation struct {
	parent, child, column string
	setNull               bool   // on_delete=SET_NULL
	soft                  bool   // the child has deleted_at
	filter                string // rows the child's manager hides beyond deleted ones
}

var byParent = func() map[string][]relation {
	m := map[string][]relation{}
	for _, r := range relations {
		m[r.parent] = append(m[r.parent], r)
	}
	return m
}()

// Row is instance.delete() for one row of table: it sets deleted_at (and
// save()'s updated_at/updated_by), then cascades. actor is the requesting
// user, whom the inline task's save() calls record as updated_by.
func Row(ctx context.Context, q db.Querier, table string, id uuid.UUID, actor uuid.UUID) error {
	tag, err := q.Exec(ctx, fmt.Sprintf(
		`UPDATE %s SET deleted_at = now(), updated_at = now(), updated_by_id = $2 WHERE id = $1 AND deleted_at IS NULL`,
		pgx.Identifier{table}.Sanitize()), id, actor)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return Cascade(ctx, q, table, []uuid.UUID{id}, actor)
}

// Cascade soft-deletes the live rows referencing ids (rows of table that
// were just deleted), recursively.
func Cascade(ctx context.Context, q db.Querier, table string, ids []uuid.UUID, actor uuid.UUID) error {
	for _, r := range byParent[table] {
		child, col := pgx.Identifier{r.child}.Sanitize(), pgx.Identifier{r.column}.Sanitize()
		if r.setNull {
			live := ""
			if r.soft {
				live = " AND deleted_at IS NULL" // the reverse manager hides deleted rows
			}
			if r.filter != "" {
				live += " AND " + r.filter
			}
			if _, err := q.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s = NULL WHERE %s = ANY($1)%s`, child, col, col, live), ids); err != nil {
				return err
			}
			continue
		}
		// Children are read through their `objects` manager, so e.g. a
		// project's Triage state (StateManager hides it) stays live.
		filter := ""
		if r.filter != "" {
			filter = " AND " + r.filter
		}
		update := fmt.Sprintf(`UPDATE %s SET deleted_at = now(), updated_at = now(), updated_by_id = $2
			WHERE %s = ANY($1) AND deleted_at IS NULL%s`, child, col, filter)
		if len(byParent[r.child]) == 0 {
			// A leaf (some, like project_identifiers, have integer ids).
			if _, err := q.Exec(ctx, update, ids, actor); err != nil {
				return err
			}
			continue
		}
		rows, err := q.Query(ctx, update+" RETURNING id", ids, actor)
		if err != nil {
			return err
		}
		childIDs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return err
		}
		if len(childIDs) > 0 {
			if err := Cascade(ctx, q, r.child, childIDs, actor); err != nil {
				return err
			}
		}
	}
	return nil
}
