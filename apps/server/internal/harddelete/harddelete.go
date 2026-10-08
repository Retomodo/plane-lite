// Package harddelete ports Django's deletion Collector for a queryset
// .delete() through a plain manager (Model.all_objects): the rows are
// removed from Postgres, every CASCADE child (soft-deleted or not) goes with
// them, and SET_NULL children are nulled.
//
// Deliberate deviation: DO_NOTHING children are deleted too, like CASCADE
// ones. Plane's foreign keys are DEFERRABLE INITIALLY DEFERRED without ON
// DELETE, so in Django a DO_NOTHING child still pointing at a deleted row
// fails the commit with an IntegrityError. The only such children are
// issue_activities rows (issue_id, issue_comment_id), so Django's nightly
// hard_delete stops for good at the first deleted issue or comment past the
// cutoff. Purging those activities with their issue or comment lets the run
// complete.
package harddelete

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type onDelete int

const (
	cascade onDelete = iota
	setNull
	doNothing
)

type relation struct {
	parent, child, column string
	onDelete              onDelete
}

var byParent = func() map[string][]relation {
	m := map[string][]relation{}
	for _, r := range relations {
		m[r.parent] = append(m[r.parent], r)
	}
	return m
}()

func ident(s string) string { return pgx.Identifier{s}.Sanitize() }

// anyOf matches column against $1, a text[] of the referenced table's keys.
func anyOf(column, table string) string {
	return fmt.Sprintf("%s = ANY($1::text[]::%s[])", ident(column), pks[table][1])
}

// Where deletes table's rows matching where (SQL over the table's columns,
// with args) and everything Django's Collector would delete with them, in
// one transaction. It returns how many root rows were deleted.
func Where(ctx context.Context, pool *pgxpool.Pool, table, where string, args ...any) (int, error) {
	pk, ok := pks[table]
	if !ok {
		return 0, fmt.Errorf("harddelete: unknown table %s", table)
	}
	n := 0
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT %s::text FROM %s WHERE %s`, ident(pk[0]), ident(table), where), args...)
		if err != nil {
			return err
		}
		roots, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil || len(roots) == 0 {
			return err
		}
		n = len(roots)
		return collect(ctx, tx, table, roots)
	})
	return n, err
}

type nulling struct {
	rel relation
	ids []string
}

func collect(ctx context.Context, tx pgx.Tx, table string, roots []string) error {
	doomed := map[string]map[string]bool{}
	var order []string // tables in the order first reached
	var nulls []nulling
	type batch struct {
		table string
		ids   []string
	}
	add := func(table string, ids []string) []string {
		set := doomed[table]
		if set == nil {
			set = map[string]bool{}
			doomed[table] = set
			order = append(order, table)
		}
		var fresh []string
		for _, id := range ids {
			if !set[id] {
				set[id] = true
				fresh = append(fresh, id)
			}
		}
		return fresh
	}
	queue := []batch{{table, add(table, roots)}}
	for len(queue) > 0 {
		b := queue[0]
		queue = queue[1:]
		for _, r := range byParent[b.table] {
			switch r.onDelete {
			case setNull:
				nulls = append(nulls, nulling{r, b.ids})
			case cascade, doNothing: // doNothing: see the package comment
				rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT %s::text FROM %s WHERE %s`,
					ident(pks[r.child][0]), ident(r.child), anyOf(r.column, b.table)), b.ids)
				if err != nil {
					return err
				}
				ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
				if err != nil {
					return err
				}
				if fresh := add(r.child, ids); len(fresh) > 0 {
					queue = append(queue, batch{r.child, fresh})
				}
			}
		}
	}
	// The Collector runs the SET_NULL updates before the deletes.
	for _, u := range nulls {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s = NULL WHERE %s`,
			ident(u.rel.child), ident(u.rel.column), anyOf(u.rel.column, u.rel.parent)), u.ids); err != nil {
			return err
		}
	}
	for _, t := range order {
		ids := make([]string, 0, len(doomed[t]))
		for id := range doomed[t] {
			ids = append(ids, id)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s`, ident(t), anyOf(pks[t][0], t)), ids); err != nil {
			return err
		}
	}
	return nil
}
