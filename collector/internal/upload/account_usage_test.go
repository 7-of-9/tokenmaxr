package upload

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/outbox"
	"github.com/7-of-9/tokenmaxr/collector/internal/store"
)

func TestAccountUsageSnapshotsUploadSplitAndRetry(t *testing.T) {
	firstID := "account-day-000"
	u, f, ob := setup(t, func(n int, req model.IngestRequest) (int, *model.IngestResponse) {
		if len(req.AccountUsage) > MaxAccountUsage {
			t.Fatal("account daily cap exceeded")
		}
		resp := &model.IngestResponse{OK: true, ServerTime: time.Now()}
		if n == 1 {
			resp.Retry = []string{firstID}
		}
		return 200, resp
	}, 0)
	var batch outbox.Batch
	for i := 0; i < 191; i++ {
		batch.AccountUsage = append(batch.AccountUsage, model.AccountUsageSnapshot{ID: fmt.Sprintf("account-day-%03d", i), TotalTokens: int64(i)})
	}
	if _, err := ob.Write(batch); err != nil {
		t.Fatal(err)
	}
	var bo store.Backoff
	res := u.Run(context.Background(), time.Now().Add(time.Minute), &bo, nil)
	if res.Err != nil || res.Accepted != 191 || res.Retried != 1 {
		t.Fatalf("result %+v", res)
	}
	if len(f.requests) != 3 {
		t.Fatalf("requests=%d", len(f.requests))
	}
	if _, pending := ob.Count(); pending != 0 {
		t.Fatalf("pending=%d", pending)
	}
	seen := 0
	for _, req := range f.requests {
		for _, row := range req.AccountUsage {
			if row.ID == firstID {
				seen++
			}
		}
	}
	if seen != 2 {
		t.Fatalf("retried snapshot sent %d times", seen)
	}
}
