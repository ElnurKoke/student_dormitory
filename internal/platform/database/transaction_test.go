package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestCanceledTransactionDoesNotRunChange(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err = WithinTx(ctx, db, func(context.Context, *sql.Tx) error { called = true; return nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("canceled transaction: called=%v, err=%v", called, err)
	}
}
