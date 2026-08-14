package calendar

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// newPromoteTx starts a transaction and returns a qtx bound to it plus a commit
// helper. That matches how the delete/reconcile paths invoke PromoteDefault
// inside their own transactions.
func newPromoteTx(t *testing.T, q *storage.Queries, db *sql.DB) (*storage.Queries, func(), func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	qtx := q.WithTx(tx)
	commit := func() {
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	rollback := func() { _ = tx.Rollback() }
	return qtx, commit, rollback
}

func TestPromoteDefault_PromotesSurvivingTarget(t *testing.T) {
	svc, q, db := newTestServiceWithDB(t)
	ctx := context.Background()

	personal, _ := svc.GetDefault(ctx)
	work, err := svc.Create(ctx, "Work", "#0284C7", "")
	if err != nil {
		t.Fatalf("Create Work: %v", err)
	}

	qtx, commit, rollback := newPromoteTx(t, q, db)
	defer rollback()
	if err := PromoteDefault(ctx, qtx, map[int64]struct{}{personal.ID: {}}, work.ID); err != nil {
		t.Fatalf("PromoteDefault: %v", err)
	}
	commit()

	def, err := svc.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
	if def.ID != work.ID || !def.IsDefault {
		t.Fatalf("default = %+v, want id=%d IsDefault=true", def, work.ID)
	}
}

func TestPromoteDefault_ZeroTargetRequiresPromotion(t *testing.T) {
	svc, q, db := newTestServiceWithDB(t)
	ctx := context.Background()
	personal, _ := svc.GetDefault(ctx)

	qtx, _, rollback := newPromoteTx(t, q, db)
	defer rollback()
	err := PromoteDefault(ctx, qtx, map[int64]struct{}{personal.ID: {}}, 0)
	if !errors.Is(err, ErrDefaultCalendarRequiresPromotion) {
		t.Fatalf("err = %v, want ErrDefaultCalendarRequiresPromotion", err)
	}
}

func TestPromoteDefault_RemovedTargetIsInvalid(t *testing.T) {
	svc, q, db := newTestServiceWithDB(t)
	ctx := context.Background()
	personal, _ := svc.GetDefault(ctx)

	qtx, _, rollback := newPromoteTx(t, q, db)
	defer rollback()
	// personal is among the calendars being removed, so it cannot be its own
	// replacement — the surviving-target check must reject it.
	err := PromoteDefault(ctx, qtx, map[int64]struct{}{personal.ID: {}}, personal.ID)
	if !errors.Is(err, ErrInvalidPromotionTarget) {
		t.Fatalf("err = %v, want ErrInvalidPromotionTarget", err)
	}
}

func TestPromoteDefault_NonexistentTargetIsInvalid(t *testing.T) {
	svc, q, db := newTestServiceWithDB(t)
	ctx := context.Background()
	personal, _ := svc.GetDefault(ctx)

	qtx, _, rollback := newPromoteTx(t, q, db)
	defer rollback()
	err := PromoteDefault(ctx, qtx, map[int64]struct{}{personal.ID: {}}, 9999)
	if !errors.Is(err, ErrInvalidPromotionTarget) {
		t.Fatalf("err = %v, want ErrInvalidPromotionTarget", err)
	}
}

// TestPromoteDefault_FailureLeavesDefaultIntact locks the rollback half of the
// invariant. When the target does not survive, the helper refuses before the
// caller commits. The committed default is then never observed as gone.
func TestPromoteDefault_FailureLeavesDefaultIntact(t *testing.T) {
	svc, q, db := newTestServiceWithDB(t)
	ctx := context.Background()
	personal, _ := svc.GetDefault(ctx)

	qtx, _, rollback := newPromoteTx(t, q, db)
	if err := PromoteDefault(ctx, qtx, map[int64]struct{}{personal.ID: {}}, 9999); !errors.Is(err, ErrInvalidPromotionTarget) {
		t.Fatalf("err = %v, want ErrInvalidPromotionTarget", err)
	}
	rollback()

	def, err := svc.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault after refused promotion: %v", err)
	}
	if def.ID != personal.ID {
		t.Fatalf("default = %d, want %d (must survive a refused promotion)", def.ID, personal.ID)
	}
}

// TestPromoteDefault_AcceptsTargetCreatedInSameTransaction documents why the
// survival check runs through qtx. ReconcileSelection promotes a calendar it
// just created in the same transaction. That calendar is invisible outside it.
func TestPromoteDefault_AcceptsTargetCreatedInSameTransaction(t *testing.T) {
	svc, q, db := newTestServiceWithDB(t)
	ctx := context.Background()
	personal, _ := svc.GetDefault(ctx)

	qtx, commit, rollback := newPromoteTx(t, q, db)
	defer rollback()
	created, err := qtx.CreateCalendar(ctx, storage.CreateCalendarParams{
		Name:  "Newly Added",
		Color: "#10B981",
	})
	if err != nil {
		t.Fatalf("CreateCalendar in tx: %v", err)
	}
	if err := PromoteDefault(ctx, qtx, map[int64]struct{}{personal.ID: {}}, created.ID); err != nil {
		t.Fatalf("PromoteDefault for in-tx target: %v", err)
	}
	commit()

	def, err := svc.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
	if def.ID != created.ID {
		t.Fatalf("default = %d, want %d", def.ID, created.ID)
	}
}
