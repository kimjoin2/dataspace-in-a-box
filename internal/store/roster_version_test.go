package store

import (
	"strings"
	"testing"
)

func mustOpen(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// A roster older than one this connector has already run is refused. That is
// the whole point of recording anything: without it, an operator handed a
// superseded roster at restart cannot tell.
func TestRecordRosterVersionRefusesARollback(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)
	if err := s.RecordRosterVersion(4); err != nil {
		t.Fatalf("record 4: %v", err)
	}
	err := s.RecordRosterVersion(3)
	if err == nil {
		t.Fatal("version 3 was accepted after version 4 had been recorded; " +
			"a rollback to a superseded roster would boot silently")
	}
	// The message names both, because an operator meeting this at boot has to
	// know what to re-issue above.
	for _, want := range []string{"3", "4"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// An ordinary restart presents the same roster it presented last time. If
// equal were refused, every connector would fail to boot the second time.
func TestRecordRosterVersionAcceptsAnEqualVersion(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)
	for restart := 0; restart < 3; restart++ {
		if err := s.RecordRosterVersion(2); err != nil {
			t.Fatalf("restart %d with an unchanged roster: %v", restart, err)
		}
	}
}

// The upsert must advance the stored highest on a strictly higher version,
// not merely avoid erroring. Record 2, then record 5, then try 4: if the
// insert used ON CONFLICT DO NOTHING instead of DO UPDATE, recording 5 would
// report success while leaving the stored value at 2, and 4 would then be
// accepted too — a rollback slipping through silently, the exact failure
// this milestone exists to prevent.
func TestRecordRosterVersionAdvancesTheStoredHighest(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)
	if err := s.RecordRosterVersion(2); err != nil {
		t.Fatalf("record 2: %v", err)
	}
	if err := s.RecordRosterVersion(5); err != nil {
		t.Fatalf("record 5: %v", err)
	}
	err := s.RecordRosterVersion(4)
	if err == nil {
		t.Fatal("version 4 was accepted; the stored value did not advance past 2")
	}
	if !strings.Contains(err.Error(), "5") {
		t.Errorf("error %q does not name 5; the stored value may not have advanced past 2", err)
	}
}

// The table holds one row by constraint, not by convention: CHECK (id = 1)
// keeps id = 1 the only row a writer can ever produce, which is what makes
// the id = 1 upsert in RecordRosterVersion the whole story of how this
// table changes.
func TestRosterVersionTableHoldsOneRow(t *testing.T) {
	t.Parallel()
	s := mustOpen(t)
	if err := s.RecordRosterVersion(7); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO roster_version (id, highest) VALUES (2, 99)`); err == nil {
		t.Error("a second row was accepted; the table no longer holds exactly one row")
	}
	// id is the rowid alias, so an omitted id takes the next rowid and fails
	// the check too. Worth pinning: the naive insert would work exactly once.
	if _, err := s.db.Exec(`INSERT INTO roster_version (highest) VALUES (99)`); err == nil {
		t.Error("a row with an omitted id was accepted")
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM roster_version`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("row count = %d, want 1", n)
	}
}
