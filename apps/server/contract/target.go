package contract

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"plane-lite/server/internal/config"
	"plane-lite/server/internal/db"
	"plane-lite/server/internal/jobs"
	"plane-lite/server/internal/server"
)

// target is the server under test plus direct handles on its state.
type target struct {
	env     env
	baseURL string
	pool    *pgxpool.Pool
	rdb     *redis.Client
	jobs    *jobs.Runner // the Go server's, when verifying
}

var (
	tgtOnce sync.Once
	tgt     *target
	tgtErr  error
)

// errUnavailable marks setup failures caused by missing dev services; those
// skip the suite instead of failing it.
type errUnavailable struct{ err error }

func (e errUnavailable) Error() string { return e.err.Error() }

// current returns the shared target, skipping the test when the dev
// services aren't running.
func current(t *testing.T) *target {
	t.Helper()
	tgtOnce.Do(func() { tgt, tgtErr = setup(context.Background()) })
	var unavailable errUnavailable
	if errors.As(tgtErr, &unavailable) {
		t.Skipf("contract services unavailable (docker compose -f docker-compose.dev.yml up -d): %v", tgtErr)
	}
	if tgtErr != nil {
		t.Fatalf("contract setup: %v", tgtErr)
	}
	return tgt
}

func setup(ctx context.Context) (*target, error) {
	e := loadEnv()
	pool, err := db.Open(ctx, e.databaseURL, 4)
	if err != nil {
		return nil, errUnavailable{err}
	}
	ropt, err := redis.ParseURL(e.redisURL)
	if err != nil {
		return nil, err
	}
	rdb := redis.NewClient(ropt)
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, errUnavailable{fmt.Errorf("redis: %w", err)}
	}
	t := &target{env: e, pool: pool, rdb: rdb}

	if e.record {
		t.baseURL = e.referenceURL
		resp, err := http.Get(t.baseURL + "/api/instances/")
		if err != nil {
			return nil, errUnavailable{fmt.Errorf("reference server: %w", err)}
		}
		resp.Body.Close()
		return t, nil
	}

	if err := db.Migrate(ctx, pool); err != nil {
		return nil, err
	}
	cfg := &config.Config{
		SecretKey:        SecretKey,
		DatabaseURL:      e.databaseURL,
		DatabaseMaxConns: 8,
		RedisURL:         e.redisURL,
		RedisKeyPrefix:   "plane:",
		WebURL:           WebURL,
		AppBaseURL:       AppBaseURL,
		AdminBaseURL:     AdminBaseURL,
		SpaceBaseURL:     SpaceBaseURL,
		// Only page duplication calls apps/live. Inside its container the
		// reference's localhost:3100 refuses connections; a port that always
		// refuses keeps Go the same even when a dev live server runs here.
		LiveBaseURL:          "http://127.0.0.1:1",
		CORSAllowedOrigins:   []string{AppBaseURL},
		SessionCookieName:    "session-id",
		SessionCookieAge:     604800,
		EnableSignup:         true,
		EnableEmailPassword:  true,
		EnableMagicLinkLogin: true,
		Email: config.Email{
			Host: e.smtpHost,
			Port: e.smtpPort,
			From: EmailFrom,
		},
		FileSizeLimit:           5242880,
		AppVersion:              AppVersion,
		InstanceChangelogURL:    "https://sites.plane.so/pages/691ef037bcfe416a902e48cb55f59891/",
		AuthenticationRateLimit: "10/minute",
		HardDeleteAfterDays:     60, // the reference's defaults
		EmailLogRetentionDays:   7,
		Storage: config.Storage{
			Endpoint:            e.s3.endpoint,
			AccessKeyID:         e.s3.accessKey,
			SecretAccessKey:     e.s3.secretKey,
			Bucket:              e.s3.bucket,
			Region:              e.s3.region,
			SignedURLExpiration: 3600,
		},
		UnuploadedAssetDeleteDays: 7,
	}
	srv, err := server.New(ctx, cfg, server.Options{InlineJobs: true})
	if err != nil {
		return nil, err
	}
	t.baseURL = httptest.NewServer(srv).URL
	t.jobs = srv.Jobs
	return t, nil
}

// keepTables survive resets: migration bookkeeping and Django's own
// registries, which the reference server caches.
var keepTables = []string{"schema_migrations", "river_migration", "django_migrations", "django_content_type", "auth_permission"}

// reset returns the database, Redis and the mail catcher to a known state:
// empty except for a configured instance row (what Django's
// register_instance plus a finished god-mode setup would leave).
func (t *target) reset(ctx context.Context) error {
	rows, err := t.pool.Query(ctx,
		`SELECT quote_ident(tablename) FROM pg_tables WHERE schemaname = 'public' AND tablename <> ALL($1)`, keepTables)
	if err != nil {
		return err
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if _, err := t.pool.Exec(ctx, "TRUNCATE "+strings.Join(tables, ", ")+" RESTART IDENTITY CASCADE"); err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	_, err = t.pool.Exec(ctx, `INSERT INTO instances (
		id, created_at, updated_at, instance_name, instance_id, current_version, latest_version,
		last_checked_at, is_telemetry_enabled, is_support_required, is_setup_done,
		is_signup_screen_visited, is_verified, domain, edition, is_test, is_current_version_deprecated
	) VALUES (
		'00000000-0000-4000-8000-000000000001', now(), now(), 'Plane Community Edition',
		'contract0instance0id0001', $1, $1, now(), true, true, true, true, false, '',
		'PLANE_COMMUNITY', false, false
	)`, AppVersion)
	if err != nil {
		return fmt.Errorf("seed instance: %w", err)
	}
	if err := t.rdb.FlushDB(ctx).Err(); err != nil {
		return fmt.Errorf("flush redis: %w", err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, t.env.mailpitURL+"/api/v1/messages", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("mailpit: %w", err)
	}
	resp.Body.Close()
	return nil
}

// Email is a message captured by Mailpit.
type Email struct {
	Subject string
	Text    string
	HTML    string
	From    struct{ Address string }
	To      []struct{ Address string }
}

var (
	codeRe = regexp.MustCompile(`\b\d{6}\b`)
	// Password-reset link parameters.
	resetParamRe = regexp.MustCompile(`(uidb64|token)=[^&\s"]+`)
)

// Code returns the first 6-digit code in the email (magic sign-in codes).
func (e Email) Code() string { return codeRe.FindString(e.Subject + " " + e.Text) }

// LatestEmail waits for the newest message sent to addr and records it as a
// step. Codes are masked and text whitespace collapsed: the plain-text part
// is derived from HTML and only its words matter.
func (s *Scenario) LatestEmail(addr string) Email {
	s.t.Helper()
	base := s.tgt.env.mailpitURL
	deadline := time.Now().Add(5 * time.Second)
	for {
		var list struct {
			Messages []struct{ ID string } `json:"messages"`
		}
		if err := getJSON(base+"/api/v1/search?query="+urlQuery("to:"+addr), &list); err != nil {
			s.t.Fatal(err)
		}
		if len(list.Messages) > 0 {
			var m Email
			if err := getJSON(base+"/api/v1/message/"+list.Messages[0].ID, &m); err != nil {
				s.t.Fatal(err)
			}
			var to []any
			for _, a := range m.To {
				to = append(to, a.Address)
			}
			s.steps = append(s.steps, Step{
				Actor:  "mail",
				Method: "EMAIL",
				Path:   addr,
				Body: map[string]any{
					"from":    m.From.Address,
					"to":      to,
					"subject": codeRe.ReplaceAllString(m.Subject, "<code>"),
					"text": s.ids.replace(resetParamRe.ReplaceAllString(
						codeRe.ReplaceAllString(strings.Join(strings.Fields(m.Text), " "), "<code>"), "$1=<masked>")),
				},
			})
			return m
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("no email to %s", addr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func getJSON(u string, v any) error {
	resp, err := http.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v, json.MatchCaseInsensitiveNames(true), json.RejectUnknownMembers(false))
}

func urlQuery(s string) string {
	return strings.NewReplacer(" ", "%20", "+", "%2B", "@", "%40", ":", "%3A", `"`, "%22").Replace(s)
}

// DBStrings runs a query against the target's database and returns the first
// column of each row as text, for reading values no response exposes (e.g.
// invitation tokens to alias).
func (s *Scenario) DBStrings(sql string, args ...any) []string {
	s.t.Helper()
	rows, err := s.tgt.pool.Query(context.Background(), sql, args...)
	if err != nil {
		s.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.t.Fatal(err)
	}
	return out
}

// DBRows records a query's rows as a step (actor "db"), for side effects no
// response shows. Rows are JSON objects with ids normalized; select
// comparisons rather than raw timestamps, whose text format differs.
func (s *Scenario) DBRows(label, sql string, args ...any) {
	s.t.Helper()
	var raw []byte
	if err := s.tgt.pool.QueryRow(context.Background(),
		`SELECT coalesce(json_agg(t), '[]') FROM (`+sql+`) t`, args...).Scan(&raw); err != nil {
		s.t.Fatal(err)
	}
	var rows any
	if err := json.Unmarshal(raw, &rows); err != nil {
		s.t.Fatal(err)
	}
	n := &normalizer{ids: s.ids, masks: map[string]bool{}, exact: map[string]bool{}, sorts: map[string]string{}, unordered: map[string]bool{}}
	s.steps = append(s.steps, Step{Actor: "db", Method: "QUERY", Path: label, Body: n.value("body", rows)})
}
