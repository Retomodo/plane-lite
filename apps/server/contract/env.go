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

	// S3InternalURL is the endpoint the reference reaches the optional
	// minio service at (and so the host its presigned URLs name).
	S3InternalURL = "http://minio:9000"
)

type env struct {
	slot         int
	record       bool
	referenceURL string
	databaseURL  string
	redisURL     string
	mailpitURL   string
	smtpHost     string
	smtpPort     int
	// s3 is the bucket the Go server stores file assets in. The asset
	// scenarios skip when it is unset.
	s3 s3Env
	// minioURL is the slot's MinIO as reached from here, which recording
	// needs (the reference stores assets there); "" without PLANE_DEV_S3=1.
	minioURL string
}

// s3Env is an object storage bucket. PLANE_DEV_S3=1 (MinIO started by
// `PLANE_DEV_S3=1 scripts/devstack.sh N up`) selects the slot's MinIO;
// CONTRACT_S3_ENDPOINT, CONTRACT_S3_ACCESS_KEY, CONTRACT_S3_SECRET_KEY,
// CONTRACT_S3_BUCKET and CONTRACT_S3_REGION select any S3-compatible bucket
// instead (e.g. a Cloudflare R2 test bucket, region "auto"), for verifying
// the Go side against it.
type s3Env struct {
	endpoint, accessKey, secretKey, bucket, region string
}

func (e s3Env) configured() bool { return e.endpoint != "" }

func loadEnv() env {
	slot, _ := strconv.Atoi(os.Getenv("CONTRACT_SLOT"))
	port := func(base int) int { return base + slot*10 }
	e := env{
		slot:         slot,
		record:       os.Getenv("CONTRACT_RECORD") == "1",
		referenceURL: getenv("CONTRACT_REFERENCE_URL", fmt.Sprintf("http://localhost:%d", port(58000))),
		mailpitURL:   getenv("CONTRACT_MAILPIT_URL", fmt.Sprintf("http://localhost:%d", port(58025))),
		smtpHost:     "localhost",
		smtpPort:     port(51025),
	}
	if os.Getenv("PLANE_DEV_S3") == "1" {
		e.minioURL = fmt.Sprintf("http://localhost:%d", port(59000))
		e.s3 = s3Env{endpoint: e.minioURL, accessKey: "plane-minio", secretKey: "plane-minio-secret",
			bucket: "uploads", region: "us-east-1"}
	}
	if v := os.Getenv("CONTRACT_S3_ENDPOINT"); v != "" {
		e.s3 = s3Env{endpoint: v, accessKey: os.Getenv("CONTRACT_S3_ACCESS_KEY"),
			secretKey: os.Getenv("CONTRACT_S3_SECRET_KEY"), bucket: getenv("CONTRACT_S3_BUCKET", "uploads"),
			region: getenv("CONTRACT_S3_REGION", "auto")}
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
