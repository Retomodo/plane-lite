package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/softdelete"
)

// IssueRelationViewSet (P/issues/<issue_id>/issue-relation/ and
// remove-relation/).

// relatedIssueValues is one row of the list's .values(*fields).
type relatedIssueValues struct {
	ID           uuid.UUID   `json:"id"`
	Name         string      `json:"name"`
	StateID      *uuid.UUID  `json:"state_id"`
	SortOrder    float64     `json:"sort_order"`
	Priority     string      `json:"priority"`
	SequenceID   int         `json:"sequence_id"`
	ProjectID    uuid.UUID   `json:"project_id"`
	LabelIDs     []uuid.UUID `json:"label_ids"`
	AssigneeIDs  []uuid.UUID `json:"assignee_ids"`
	CreatedAt    utcTime     `json:"created_at"`
	UpdatedAt    utcTime     `json:"updated_at"`
	CreatedBy    *uuid.UUID  `json:"created_by"`
	UpdatedBy    *uuid.UUID  `json:"updated_by"`
	RelationType string      `json:"relation_type"`
}

// relationIssuesSQL is the list's annotated queryset: label and assignee
// ids aggregated over joins (assignees through any project membership row
// of theirs that is active), grouped by issue, with no ORDER BY.
const relationIssuesSQL = `SELECT DISTINCT i.id, i.name, i.state_id, i.sort_order, i.priority, i.sequence_id, i.project_id,
		COALESCE(array_agg(DISTINCT il.label_id) FILTER (WHERE il.label_id IS NOT NULL AND il.deleted_at IS NULL),
			'{}'::uuid[]),
		COALESCE(array_agg(DISTINCT ia.assignee_id) FILTER (WHERE ia.assignee_id IS NOT NULL AND pm.is_active
			AND ia.deleted_at IS NULL), '{}'::uuid[]),
		i.created_at, i.updated_at, i.created_by_id, i.updated_by_id
	FROM issues i LEFT JOIN states s ON s.id = i.state_id JOIN projects p ON p.id = i.project_id
		JOIN workspaces w ON w.id = i.workspace_id
		LEFT JOIN issue_labels il ON il.issue_id = i.id
		LEFT JOIN issue_assignees ia ON ia.issue_id = i.id LEFT JOIN users u ON u.id = ia.assignee_id
		LEFT JOIN project_members pm ON pm.member_id = u.id
	WHERE `

// relationSide is one filter() of the list: the issues on side col of the
// issue's relations of type typ where it is on the other side.
type relationSide struct{ col, other, typ string }

func (r relationSide) sql() string {
	return `(` + issueBaseWhere + ` AND w.slug = $1 AND i.id IN (SELECT DISTINCT r.` + r.col + `
		FROM issue_relations r JOIN workspaces rw ON rw.id = r.workspace_id
		WHERE r.deleted_at IS NULL AND (r.issue_id = $2 OR r.related_issue_id = $2) AND rw.slug = $1
			AND r.` + r.other + ` = $2 AND r.relation_type = '` + r.typ + `'))`
}

// relationBuckets are the list's response keys and their querysets (two
// OR-ed for the symmetric types).
var relationBuckets = []struct {
	key   string
	sides []relationSide
}{
	{"blocking", []relationSide{{"issue_id", "related_issue_id", "blocked_by"}}},
	{"blocked_by", []relationSide{{"related_issue_id", "issue_id", "blocked_by"}}},
	{"duplicate", []relationSide{{"related_issue_id", "issue_id", "duplicate"}, {"issue_id", "related_issue_id", "duplicate"}}},
	{"relates_to", []relationSide{{"related_issue_id", "issue_id", "relates_to"}, {"issue_id", "related_issue_id", "relates_to"}}},
	{"start_after", []relationSide{{"issue_id", "related_issue_id", "start_before"}}},
	{"start_before", []relationSide{{"related_issue_id", "issue_id", "start_before"}}},
	{"finish_after", []relationSide{{"issue_id", "related_issue_id", "finish_before"}}},
	{"finish_before", []relationSide{{"related_issue_id", "issue_id", "finish_before"}}},
}

// listIssueRelations ports IssueRelationViewSet.list.
func (a *API) listIssueRelations(c *httpx.Ctx) error {
	issueID, err := c.UUIDParam("issue_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	out := map[string][]relatedIssueValues{}
	for _, b := range relationBuckets {
		conds := make([]string, len(b.sides))
		for i, s := range b.sides {
			conds[i] = s.sql()
		}
		rows, err := a.db.Query(ctx, relationIssuesSQL+strings.Join(conds, " OR ")+` GROUP BY i.id`, c.Param("slug"), issueID)
		if err != nil {
			return err
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (relatedIssueValues, error) {
			var (
				v                   relatedIssueValues
				created, updated    time.Time
				labelIDs, assignees []uuid.UUID
			)
			err := row.Scan(&v.ID, &v.Name, &v.StateID, &v.SortOrder, &v.Priority, &v.SequenceID, &v.ProjectID,
				&labelIDs, &assignees, &created, &updated, &v.CreatedBy, &v.UpdatedBy)
			v.LabelIDs, v.AssigneeIDs = labelIDs, assignees
			v.CreatedAt, v.UpdatedAt = utcTime(created), utcTime(updated)
			v.RelationType = b.key
			return v, err
		})
		if err != nil {
			return err
		}
		if list == nil {
			list = []relatedIssueValues{}
		}
		out[b.key] = list
	}
	return c.JSON(http.StatusOK, out)
}

// actualRelation is get_actual_relation: the type a relation is stored as.
func actualRelation(rel string) string {
	switch rel {
	case "start_after":
		return "start_before"
	case "finish_after":
		return "finish_before"
	case "blocking":
		return "blocked_by"
	case "implements":
		return "implemented_by"
	}
	return rel
}

// issueRelationOut is IssueRelationSerializer / RelatedIssueSerializer of a
// relation made by bulk_create: both show the requested issue.
type issueRelationOut struct {
	ID           uuid.UUID  `json:"id"`
	ProjectID    uuid.UUID  `json:"project_id"`
	SequenceID   int        `json:"sequence_id"`
	RelationType string     `json:"relation_type"`
	Name         string     `json:"name"`
	StateID      *uuid.UUID `json:"state_id"`
	Priority     string     `json:"priority"`
	CreatedBy    uuid.UUID  `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	UpdatedBy    uuid.UUID  `json:"updated_by"`
}

var errRelationTypeRequired = httpx.Body(http.StatusBadRequest, map[string]string{"message": "Issue relation type is required"})

// createIssueRelations ports IssueRelationViewSet.create: relate the
// requested live issues of the workspace (any project), skipping pairs that
// already have a relation. The response lists them all, skipped or not.
func (a *API) createIssueRelations(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	issueID, err := c.UUIDParam("issue_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash // request.data.get on a list
	}
	rt, ok := data.Get("relation_type")
	if !ok || rt.IsNull() {
		return errRelationTypeRequired
	}
	if k := rt.Kind(); k == '[' || k == '{' {
		return errViewCrash // get_actual_relation: an unhashable dict key
	}
	relationType := drf.PyStr(rt)
	var items []drf.Value
	if v, ok := data.Get("issues"); ok {
		switch v.Kind() {
		case '[', '"', '{':
			items, _ = pyIter(v)
		default:
			return errViewCrash // pk__in over a non-iterable
		}
	}
	ids, err := modelUUIDs(items)
	if err != nil {
		return err
	}
	type target struct {
		id, project uuid.UUID
		seq         int
		name        string
		state       *uuid.UUID
		priority    string
	}
	rows, err := a.db.Query(ctx, `SELECT i.id, i.project_id, i.sequence_id, i.name, i.state_id, i.priority
		FROM issues i JOIN workspaces w ON w.id = i.workspace_id
		WHERE w.slug = $1 AND i.id = ANY($2) AND `+issueObjects("i")+`
		ORDER BY i.created_at DESC`, c.Param("slug"), ids)
	if err != nil {
		return err
	}
	targets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (target, error) {
		var t target
		err := row.Scan(&t.id, &t.project, &t.seq, &t.name, &t.state, &t.priority)
		return t, err
	})
	if err != nil {
		return err
	}
	actual := actualRelation(relationType)
	reversed := relationType == "blocking" || relationType == "start_after" || relationType == "finish_after"
	now := time.Now()
	out := make([]issueRelationOut, 0, len(targets))
	if len(targets) > 0 {
		var issues, related []uuid.UUID
		for _, t := range targets {
			if reversed {
				issues, related = append(issues, t.id), append(related, issueID)
			} else {
				issues, related = append(issues, issueID), append(related, t.id)
			}
			out = append(out, issueRelationOut{ID: t.id, ProjectID: t.project, SequenceID: t.seq, RelationType: actual,
				Name: t.name, StateID: t.state, Priority: t.priority, CreatedBy: c.User.ID, CreatedAt: now,
				UpdatedAt: now, UpdatedBy: c.User.ID})
		}
		// bulk_create(ignore_conflicts=True): one statement, all or nothing.
		if _, err := a.db.Exec(ctx, `INSERT INTO issue_relations (id, created_at, updated_at, issue_id, related_issue_id,
				relation_type, project_id, workspace_id, created_by_id, updated_by_id)
			SELECT gen_random_uuid(), $1, $1, x.issue, x.related, $4, p.id, p.workspace_id, $6, $6
			FROM unnest($2::uuid[], $3::uuid[]) WITH ORDINALITY AS x(issue, related, n), projects p WHERE p.id = $5
			ORDER BY x.n
			ON CONFLICT DO NOTHING`, now, issues, related, actual, projectID, c.User.ID); err != nil {
			return err
		}
	}
	requested := string(data.Object())
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: "issue_relation.activity.created", RequestedData: &requested, IssueID: issueID.String(),
		ActorID: c.User.ID.String(), ProjectID: projectID.String(), Epoch: time.Now().Unix(),
		Subscriber: true, Notification: true,
	})
	return c.JSON(http.StatusCreated, out)
}

// removeIssueRelation ports IssueRelationViewSet.remove_relation: the
// newest relation between the two issues goes, whichever its direction or
// type.
func (a *API) removeIssueRelation(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	issueID, err := c.UUIDParam("issue_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash
	}
	v, ok := data.Get("related_issue")
	if !ok {
		return errViewCrash // related_issue=None matches nothing: None.delete()
	}
	ids, err := modelUUIDs([]drf.Value{v})
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return errViewCrash // related_issue=None matches nothing: None.delete()
	}
	var (
		id                             uuid.UUID
		relatedID, relProject          uuid.UUID
		relSeq                         int
		relName, relType, relPriority  string
		relState, createdBy, updatedBy *uuid.UUID
		createdAt, updatedAt           time.Time
	)
	err = a.db.QueryRow(ctx, `SELECT r.id, r.relation_type, r.created_by_id, r.updated_by_id, r.created_at, r.updated_at,
			i.id, i.project_id, i.sequence_id, i.name, i.state_id, i.priority
		FROM issue_relations r JOIN workspaces w ON w.id = r.workspace_id JOIN issues i ON i.id = r.related_issue_id
		WHERE r.deleted_at IS NULL AND w.slug = $1
			AND ((r.issue_id = $3 AND r.related_issue_id = $2) OR (r.issue_id = $2 AND r.related_issue_id = $3))
		ORDER BY r.created_at DESC LIMIT 1`, c.Param("slug"), issueID, ids[0]).
		Scan(&id, &relType, &createdBy, &updatedBy, &createdAt, &updatedAt, &relatedID, &relProject, &relSeq, &relName,
			&relState, &relPriority)
	if err == pgx.ErrNoRows {
		return errViewCrash // .first() is None: None.delete()
	}
	if err != nil {
		return err
	}
	// IssueRelationSerializer(issue_relations).data
	current, err := httpx.Marshal(map[string]any{
		"id": relatedID, "project_id": relProject, "sequence_id": relSeq, "relation_type": relType, "name": relName,
		"state_id": relState, "priority": relPriority, "created_by": createdBy, "created_at": createdAt,
		"updated_at": updatedAt, "updated_by": updatedBy,
	}, c.Loc())
	if err != nil {
		return err
	}
	if err := softdelete.Row(ctx, a.db, "issue_relations", id, c.User.ID); err != nil {
		return err
	}
	requested, cur := string(data.Object()), string(current)
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: "issue_relation.activity.deleted", RequestedData: &requested, CurrentInstance: &cur,
		IssueID: issueID.String(), ActorID: c.User.ID.String(), ProjectID: projectID.String(), Epoch: time.Now().Unix(),
		Subscriber: true, Notification: true,
	})
	return c.NoContent()
}
