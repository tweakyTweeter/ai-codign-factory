package factory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Evidence struct {
	Role        string   `json:"role,omitempty"`
	OutputDir   string   `json:"output_dir,omitempty"`
	ResultFile  string   `json:"result_file,omitempty"`
	Verdict     *Verdict `json:"verdict,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	Stage       string   `json:"stage"`
	Log         string   `json:"log"`
	ExitCode    int      `json:"exit_code"`
	Error       string   `json:"error,omitempty"`
	Truncated   bool     `json:"truncated"`
	StartedAt   string   `json:"started_at"`
	FinishedAt  string   `json:"finished_at"`
}

type Run struct {
	Integrity       *Integrity   `json:"integrity,omitempty"`
	Integration     *Integration `json:"integration,omitempty"`
	HumanReview     *HumanReview `json:"human_review,omitempty"`
	Actor           string       `json:"actor,omitempty"`
	Stage           string       `json:"stage,omitempty"`
	Role            string       `json:"role,omitempty"`
	Fingerprint     string       `json:"fingerprint,omitempty"`
	QAFingerprint   string       `json:"qa_fingerprint,omitempty"`
	ID              string       `json:"id"`
	State           string       `json:"state"`
	TaskID          string       `json:"task_id,omitempty"`
	Profile         string       `json:"profile,omitempty"`
	Head            string       `json:"head"`
	Branch          string       `json:"branch"`
	Worktree        string       `json:"worktree"`
	Dir             string       `json:"run_dir"`
	ControllerPID   int          `json:"controller_pid"`
	LaunchToken     string       `json:"launch_token"`
	WorkerPID       int          `json:"worker_pid,omitempty"`
	CreatedAt       string       `json:"created_at"`
	UpdatedAt       string       `json:"updated_at"`
	Detail          string       `json:"detail,omitempty"`
	Evidence        []Evidence   `json:"evidence"`
	CancelRequested bool         `json:"cancel_requested"`

	// LaunchResidue records Claude launch leftovers the reconcile/release
	// sweep found for this run (acf-72). Absent for runs without any.
	LaunchResidue []LaunchResidue `json:"launch_residue,omitempty"`
}

// SaveLaunchResidue journals swept launch leftovers on a run without changing
// its state, active flag or cancellation. The caller holds the controller lock.
func (s *Store) SaveLaunchResidue(ctx context.Context, r *Run, detail string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var requested bool
	if err := tx.QueryRowContext(ctx, "SELECT cancel_requested FROM runs WHERE id=?", r.ID).Scan(&requested); err != nil {
		return err
	}
	r.CancelRequested = requested
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE runs SET data=? WHERE id=?", data, r.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO events(run_id,state,detail,at) VALUES(?,?,?,?)",
		r.ID, "launch_residue", detail, timestamp()); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveHumanReview journals the bridge independently of run lifecycle/release.
// Cancellation is serialized with the decision write and always wins approval.
// The caller holds the repository controller lock.
func (s *Store) SaveHumanReview(ctx context.Context, r *Run, detail string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var requested bool
	if err := tx.QueryRowContext(ctx, "SELECT cancel_requested FROM runs WHERE id=?", r.ID).Scan(&requested); err != nil {
		return err
	}
	r.CancelRequested = requested
	if requested && r.HumanReview != nil {
		r.HumanReview.Decision = "not-approved"
		r.HumanReview.Detail = "cancellation requested"
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE runs SET data=? WHERE id=?", data, r.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO events(run_id,state,detail,at) VALUES(?,?,?,?)",
		r.ID, "human_review", detail, timestamp()); err != nil {
		return err
	}
	return tx.Commit()
}

type Event struct {
	Sequence int64  `json:"sequence"`
	State    string `json:"state"`
	Detail   string `json:"detail"`
	At       string `json:"at"`
}

type Store struct{ db *sql.DB }

func Open(ctx context.Context, r Repo) (*Store, error) {
	if err := os.MkdirAll(r.State, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(r.State, "runs.sqlite")
	// Pre-create privately; SQLite's default file mode otherwise follows umask.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure journal timeout: %w", err)
	}
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, fmt.Errorf("read journal version: %w", err)
	}
	if version != 0 && version != 1 {
		db.Close()
		return nil, fmt.Errorf("unsupported journal version %d; refusing to mutate it", version)
	}
	_, err = db.ExecContext(ctx, `
PRAGMA busy_timeout=5000;
PRAGMA journal_mode=DELETE;
PRAGMA synchronous=FULL;
CREATE TABLE IF NOT EXISTS runs (
 id TEXT PRIMARY KEY, state TEXT NOT NULL, active INTEGER NOT NULL,
 data BLOB NOT NULL, cancel_requested INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_run ON runs(active) WHERE active=1;
CREATE TABLE IF NOT EXISTS events (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL,
 state TEXT NOT NULL, detail TEXT NOT NULL, at TEXT NOT NULL
);
PRAGMA user_version=1;`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize journal: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Save atomically journals a transition with its durable snapshot before callers
// proceed to the associated side effect. Only a lock-holding controller calls it.
func (s *Store) Save(ctx context.Context, r *Run, state, detail string, create bool) error {
	copyRun := *r
	copyRun.State, copyRun.Detail, copyRun.UpdatedAt = state, detail, timestamp()
	data, err := json.Marshal(copyRun)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Serialize the human gate with durable cancellation. A separate pre-gate
	// read leaves a window where a committed cancel could otherwise become a
	// successful awaiting_review transition.
	if state == "awaiting_review" || state == "released" && r.Integration != nil && r.Integration.State == "complete" {
		var requested bool
		if err := tx.QueryRowContext(ctx, "SELECT cancel_requested FROM runs WHERE id=?", r.ID).Scan(&requested); err != nil {
			return err
		}
		if requested {
			return context.Canceled
		}
	}
	active := 1
	if state == "released" {
		active = 0
	}
	if create {
		_, err = tx.ExecContext(ctx, "INSERT INTO runs(id,state,active,data) VALUES(?,?,?,?)", r.ID, state, active, data)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE runs SET state=?,active=?,data=? WHERE id=?", state, active, data, r.ID)
	}
	if err != nil {
		return fmt.Errorf("save run (an active run requires release): %w", err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO events(run_id,state,detail,at) VALUES(?,?,?,?)", r.ID, state, detail, copyRun.UpdatedAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*r = copyRun
	return nil
}

func (s *Store) Get(ctx context.Context, id string) (Run, error) {
	var data []byte
	var cancel bool
	err := s.db.QueryRowContext(ctx, "SELECT data,cancel_requested FROM runs WHERE id=?", id).Scan(&data, &cancel)
	if err != nil {
		return Run{}, fmt.Errorf("get run %s: %w", id, err)
	}
	var r Run
	err = json.Unmarshal(data, &r)
	r.CancelRequested = cancel
	return r, err
}

func (s *Store) List(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT data,cancel_requested FROM runs ORDER BY rowid DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Run{}
	for rows.Next() {
		var data []byte
		var cancel bool
		if err := rows.Scan(&data, &cancel); err != nil {
			return nil, err
		}
		var r Run
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, err
		}
		r.CancelRequested = cancel
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) Active(ctx context.Context) (Run, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM runs WHERE active=1").Scan(&id)
	if err != nil {
		return Run{}, err
	}
	return s.Get(ctx, id)
}

func (s *Store) Events(ctx context.Context, id string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT sequence,state,detail,at FROM events WHERE run_id=? ORDER BY sequence", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Sequence, &e.State, &e.Detail, &e.At); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func (s *Store) RequestCancel(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "UPDATE runs SET cancel_requested=1 WHERE id=? AND active=1", id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("run not found or already released")
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO events(run_id,state,detail,at) VALUES(?,?,?,?)",
		id, "cancel_requested", "durable cooperative cancellation requested; no stored PID signalled", timestamp()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CancelRequested(ctx context.Context, id string) (bool, error) {
	var requested bool
	err := s.db.QueryRowContext(ctx, "SELECT cancel_requested FROM runs WHERE id=?", id).Scan(&requested)
	return requested, err
}
