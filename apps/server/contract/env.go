// Package contract checks that the Go server reproduces the Django API.
//
// A scenario is a scripted series of HTTP calls. In record mode
// (CONTRACT_RECORD=1) it runs against the Django reference server and saves
// normalized responses to testdata/golden. In verify mode (the default) it
// runs against the Go server, in-process, and must reproduce those goldens.
//
// Both modes need the dev services:
//
//	docker compose -f docker-compose.dev.yml up -d                       # verify
//	docker compose -f docker-compose.dev.yml --profile reference up -d   # record
//
// CONTRACT_SLOT=N targets the isolated stack scripts/devstack.sh N runs, whose
// ports are the defaults plus N*10.
package contract

import (
	"fmt"
	"os"
	"strconv"
)

// Settings shared by the Django reference (docker-compose.dev.yml) and the
// in-process Go server. Keep these two in sync with the compose file.
const (
	AppBaseURL   = "http://localhost:3000"
	AdminBaseURL = "http://localhost:3001"
	SpaceBaseURL = "http://localhost:3002"
	LiveBaseURL  = "http://localhost:3100"
	WebURL       = "http://localhost:58000"
	SecretKey    = "reference-secret-key-not-for-production-use-0123456789"
	EmailFrom    = "Plane <plane@example.com>"
	AppVersion   = "v1.4.2"
)

type env struct {
	record       bool
	referenceURL string
	databaseURL  string
	redisURL     string
	mailpitURL   string
	smtpHost     string
	smtpPort     int
}

func loadEnv() env {
	slot, _ := strconv.Atoi(os.Getenv("CONTRACT_SLOT"))
	port := func(base int) int { return base + slot*10 }
	e := env{
		record:       os.Getenv("CONTRACT_RECORD") == "1",
		referenceURL: getenv("CONTRACT_REFERENCE_URL", fmt.Sprintf("http://localhost:%d", port(58000))),
		mailpitURL:   getenv("CONTRACT_MAILPIT_URL", fmt.Sprintf("http://localhost:%d", port(58025))),
		smtpHost:     "localhost",
		smtpPort:     port(51025),
	}
	db := fmt.Sprintf("postgres://plane:plane@localhost:%d/", port(55432))
	redis := fmt.Sprintf("redis://localhost:%d/", port(56379))
	if e.record {
		e.databaseURL = getenv("CONTRACT_REFERENCE_DATABASE_URL", db+"plane_ref")
		e.redisURL = getenv("CONTRACT_REFERENCE_REDIS_URL", redis+"1")
	} else {
		e.databaseURL = getenv("CONTRACT_DATABASE_URL", db+"plane_test")
		e.redisURL = getenv("CONTRACT_REDIS_URL", redis+"2")
	}
	return e
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
