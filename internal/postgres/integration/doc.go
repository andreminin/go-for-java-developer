// Package integration holds live-PostgreSQL tests for Module 2.
//
// These tests close the loop between "the SQL strings quoted in the examples"
// and "the SQL that actually runs". They need the compose database:
//
//	docker compose up -d db
//	docker compose exec -T db psql -U app -d app -v ON_ERROR_STOP=1 \
//	  -f /testdata/migrations/001_init.sql
//	go test -tags=integration -race ./internal/postgres/integration/
//
// If the database is unreachable the tests Skip instead of failing, so plain
// `go test ./...` (without the tag or the container) stays green.
//
// NOTE: this package imports github.com/lib/pq, a database/sql driver used
// ONLY by the build-tagged integration tests. The runnable examples stay
// standard-library-only. If you run `go mod tidy`, include the tag or the
// driver is dropped from go.mod: `go mod tidy -tags=integration`.
package integration
