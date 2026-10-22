// Package synclock serializes sync and account lifecycle changes.
package synclock

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/douglasdemoura/chroncal/internal/fileid"
	"github.com/gofrs/flock"
)

type calendarKey struct {
	db         *sql.DB
	calendarID int64
}

type accountKey struct {
	db        *sql.DB
	accountID int64
}

var (
	mu               sync.Mutex
	calendarLocks    = map[calendarKey]*sync.Mutex{}
	localAccountLock = map[accountKey]chan struct{}{}
)

// Calendar returns the process-local lock that serializes complete sync cycles
// for one calendar. Account lifecycle operations use Account instead. Every
// sync and account mutation must acquire that broader lock first.
func Calendar(db *sql.DB, calendarID int64) *sync.Mutex {
	key := calendarKey{db: db, calendarID: calendarID}
	mu.Lock()
	defer mu.Unlock()
	lock, ok := calendarLocks[key]
	if !ok {
		lock = &sync.Mutex{}
		calendarLocks[key] = lock
	}
	return lock
}

// Account acquires the lifecycle lock for an account and returns its release
// function. A process-local semaphore coordinates callers that share one
// *sql.DB. A file lock beside the SQLite database coordinates independent
// chroncal processes. In-memory databases need only the process-local half.
func Account(ctx context.Context, db *sql.DB, accountID int64) (func(), error) {
	local := processAccountLock(db, accountID)
	select {
	case local <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	releaseLocal := func() { <-local }

	dbPath, err := mainDatabasePath(ctx, db)
	if err != nil {
		releaseLocal()
		return nil, err
	}
	if dbPath == "" {
		return onceRelease(releaseLocal), nil
	}

	lockPath, err := accountLockPath(dbPath, accountID)
	if err != nil {
		releaseLocal()
		return nil, err
	}
	fileLock := flock.New(lockPath)
	locked, err := fileLock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		releaseLocal()
		return nil, fmt.Errorf("lock account %d: %w", accountID, err)
	}
	if !locked {
		releaseLocal()
		return nil, fmt.Errorf("lock account %d: lock not acquired", accountID)
	}
	return onceRelease(func() {
		_ = fileLock.Unlock()
		releaseLocal()
	}), nil
}

func processAccountLock(db *sql.DB, accountID int64) chan struct{} {
	key := accountKey{db: db, accountID: accountID}
	mu.Lock()
	defer mu.Unlock()
	lock, ok := localAccountLock[key]
	if !ok {
		lock = make(chan struct{}, 1)
		localAccountLock[key] = lock
	}
	return lock
}

func accountLockPath(dbPath string, accountID int64) (string, error) {
	identity, err := fileid.Identity(dbPath)
	if err != nil {
		return "", fmt.Errorf("identify SQLite database for locking: %w", err)
	}
	lockDir, err := accountLockRoot()
	if err != nil {
		return "", err
	}
	identityHash := sha256.Sum256([]byte(identity))
	return filepath.Join(lockDir, fmt.Sprintf("%x.account-%d.lock", identityHash, accountID)), nil
}

func mainDatabasePath(ctx context.Context, db *sql.DB) (string, error) {
	var (
		seq  int
		name string
		path string
	)
	if err := db.QueryRowContext(ctx, "PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		return "", fmt.Errorf("identify SQLite database: %w", err)
	}
	if path == "" {
		return "", nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve SQLite database path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}
	return absolute, nil
}

func onceRelease(release func()) func() {
	var once sync.Once
	return func() { once.Do(release) }
}

// Calendars locks a set of calendars. resolve maps each calendar ID to
// lock to its lock ID: the account ID, or the negative calendar ID for a
// calendar without an account. Calendars takes the account locks in
// order of lock ID, then the calendar locks in order of calendar ID. This
// order prevents a deadlock between two callers.
//
// After the locks, Calendars calls resolve again. When the result
// changed, it releases the locks and tries again. The returned function
// releases every lock.
func Calendars(ctx context.Context, db *sql.DB, resolve func(context.Context) (map[int64]int64, error)) (func(), error) {
	for {
		before, err := resolve(ctx)
		if err != nil {
			return nil, err
		}
		lockIDs := slices.Sorted(maps.Values(before))
		lockIDs = slices.Compact(lockIDs)
		calendarIDs := slices.Sorted(maps.Keys(before))

		var releases []func()
		releaseAll := func() {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
		}
		for _, id := range lockIDs {
			release, err := Account(ctx, db, id)
			if err != nil {
				releaseAll()
				return nil, fmt.Errorf("lock sync account: %w", err)
			}
			releases = append(releases, release)
		}
		for _, id := range calendarIDs {
			lock := Calendar(db, id)
			lock.Lock()
			releases = append(releases, lock.Unlock)
		}
		after, err := resolve(ctx)
		if err != nil {
			releaseAll()
			return nil, fmt.Errorf("revalidate calendar lifecycle lock: %w", err)
		}
		if !maps.Equal(before, after) {
			releaseAll()
			continue
		}
		return releaseAll, nil
	}
}
