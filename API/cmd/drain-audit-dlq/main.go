// Command drain-audit-dlq replays rows from the audit_dlqs dead-letter queue
// back into audit_logs. It is safe to run repeatedly — each successful replay
// deletes the DLQ row so the same event cannot be inserted twice.
//
// Usage:
//
//	go run ./cmd/drain-audit-dlq                  # drain all rows, batch=100
//	go run ./cmd/drain-audit-dlq -batch 50        # smaller batches
//	go run ./cmd/drain-audit-dlq -dry-run         # print what would be drained, no writes
//
// Exit codes: 0 on full success, 1 if any row could not be replayed.
package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"

	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/joho/godotenv"
)

func main() {
	os.Exit(run())
}

func run() int {
	batch := flag.Int("batch", 100, "DLQ rows to process per iteration")
	dryRun := flag.Bool("dry-run", false, "print rows that would be drained without writing")
	flag.Parse()

	_ = godotenv.Load()

	dbCfg, err := db.Load()
	if err != nil {
		log.Fatalf("load db config: %v", err)
	}

	handle, err := db.Open(dbCfg)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer func() {
		if cErr := handle.Close(); cErr != nil {
			slog.Warn("close db", "error", cErr)
		}
	}()

	ctx := context.Background()
	drained, failed := drain(ctx, handle, *batch, *dryRun)
	slog.Info("done", "drained", drained, "failed", failed, "dry_run", *dryRun)
	if failed > 0 {
		return 1
	}
	return 0
}

// drain iterates the DLQ in batches ordered by failed_at ASC, re-inserts each
// row into audit_logs via the AuditWriter pool (so BeforeCreate handles the
// advisory lock and hash-chain), then deletes the DLQ row on success.
// Returns the count of successfully drained rows and the count of failures.
func drain(ctx context.Context, handle *db.Handle, batch int, dryRun bool) (drained, failed int) {
	for {
		var rows []models.AuditDLQ
		if err := handle.RW.WithContext(ctx).
			Order("failed_at ASC").
			Limit(batch).
			Find(&rows).Error; err != nil {
			slog.Error("fetch DLQ batch", "error", err)
			failed++
			return
		}
		if len(rows) == 0 {
			return
		}

		for _, row := range rows {
			if dryRun {
				slog.Info("dry-run",
					"id", row.ID, "event", row.EventType,
					"subject_type", row.SubjectType, "subject_id", row.SubjectID,
					"failed_at", row.FailedAt.Format("2006-01-02T15:04:05Z"),
					"write_error", row.WriteError)
				drained++
				continue
			}

			entry := &models.AuditLog{
				EventType:   row.EventType,
				SubjectType: row.SubjectType,
				SubjectID:   row.SubjectID,
				Payload:     row.Payload,
			}

			if err := handle.AuditWriter.WithContext(ctx).Create(entry).Error; err != nil {
				slog.Error("replay dlq", "error", err, "id", row.ID, "event", row.EventType)
				failed++
				continue
			}

			if err := handle.RW.WithContext(ctx).Delete(&row).Error; err != nil {
				slog.Error("delete dlq after replay", "error", err, "id", row.ID)
				failed++
				continue
			}

			slog.Info("drained", "id", row.ID, "event", row.EventType, "audit_log_id", entry.ID)
			drained++
		}

		if len(rows) < batch {
			return
		}
	}
}
