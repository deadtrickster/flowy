package main

// `flowy fuse` - the agent filesystem.
//
// Phase 7, and the shape of it is the point: this is opt-in. Nothing mounts
// unless somebody runs this command, and the node with nothing mounted is the
// node every earlier phase describes - chat, the API, the memory tools, the
// merge, all of it, with memory living in the store and no file view over it.
// What the mount adds is one thing: an agent writes a file where it already
// writes files, and the file is a memory item in the store, signed, indexed and
// searchable by every other agent on the fabric.
//
// The token is what decides whose memory this is. A mount is one principal's
// view - its own personal memory, and the project memory it may read - and the
// permission filter it is built out of is the same one the API is narrowed by.
// There is no mount of everything, and the operator's ?scope=all does not reach
// in here: a filesystem that showed a project's memory to whoever ran the
// process would be a way round the boundary rather than a view of it.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/agentfs"
	"github.com/deadtrickster/flowy/internal/store"
)

// fuseCmd runs `flowy fuse`.
func fuseCmd(args []string) error {
	fs := flag.NewFlagSet("fuse", flag.ContinueOnError)
	mountpoint := fs.String("mount", "", "directory to mount the memory filesystem on")
	token := fs.String("token", os.Getenv("FLOWY_TOKEN"), "bearer token whose principal the mount is of (default $FLOWY_TOKEN)")
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres-wire DSN (default $DATABASE_URL)")
	node := fs.String("node", envOr("FLOWY_NODE", defaultNode()), "name of this node, stamped onto every row")
	reconcile := fs.Bool("reconcile", false, "apply the queued writes and exit, without mounting anything")
	noDrain := fs.Bool("no-drain", false, "mount without a drainer: writes are queued and left there")
	debug := fs.Bool("debug", false, "log the FUSE protocol traffic")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("no DSN: set DATABASE_URL or pass -dsn")
	}
	if *mountpoint == "" && !*reconcile {
		return errors.New("nothing to do: pass --mount <dir> to mount, or --reconcile to " +
			"apply what an earlier mount queued")
	}

	logger := log.New(os.Stderr, "fuse: ", log.LstdFlags)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	db, err := store.Open(dialCtx, *dsn, *node)
	if err != nil {
		return err
	}
	defer db.Close()

	// The drainer writes artifacts, so it meets the same rule every other write
	// path does - see serve, where this is explained.
	if _, err := db.BackfillProjects(dialCtx); err != nil {
		return fmt.Errorf("projects: %w", err)
	}

	if *mountpoint == "" {
		return reconcileQueue(ctx, db, logger)
	}
	return mountFS(ctx, db, logger, *token, *mountpoint, *debug, !*noDrain)
}

// reconcileQueue is `flowy fuse --reconcile`: apply whatever a mount queued and
// exit.
//
// It takes no token, and that is deliberate rather than an oversight. Every
// intent carries the owner, the actor, the project and the scope that were
// decided when the file was closed - by a principal that had already been
// checked - so replaying one needs no credential and must not be able to
// acquire one. What it can still do is refuse: the apply re-checks ownership
// and the personal floor against the row as it is now, inside the transaction
// that would do the write.
func reconcileQueue(ctx context.Context, db *store.DB, logger *log.Logger) error {
	stats, err := agentfs.NewDrainer(db, logger).Reconcile(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("reconciled %d queued write(s): %s\n", stats.Total(), stats)
	return nil
}

// mountFS is `flowy fuse --mount`: resolve the principal, mount, serve until a
// signal, unmount.
func mountFS(ctx context.Context, db *store.DB, logger *log.Logger,
	token, mountpoint string, debug, drain bool,
) error {
	p, err := principalForMount(ctx, db, token)
	if err != nil {
		return err
	}

	mounted, err := agentfs.Mount(db, p, agentfs.Options{
		Mountpoint: mountpoint,
		Debug:      debug,
		Logger:     logger,
	})
	if err != nil {
		return err
	}

	where := agentfs.Personal
	if p.Project != "" {
		where = agentfs.Personal + " and " + p.Project
	}
	logger.Printf("flowy %s: memory of user %s at %s (%s); ^C or SIGTERM to unmount",
		version, p.UserID, mountpoint, where)

	return mounted.Serve(ctx, drain)
}

// principalForMount resolves the token the mount acts as. A mount with no
// principal is not a read-only mount, it is a mount of nothing: every directory
// in it is a permission-filtered read, and a filter with no principal is FALSE.
func principalForMount(ctx context.Context, db *store.DB, token string) (*store.Principal, error) {
	if token == "" {
		return nil, errors.New("no token: pass --token or set FLOWY_TOKEN; " +
			"the mount is one principal's memory and there is no principal without one")
	}
	p, err := db.PrincipalForToken(ctx, token)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errors.New("unknown token")
	}
	if err != nil {
		return nil, err
	}
	if p.UserID == "" {
		return nil, errors.New("this token resolves to no user, so there is no memory to mount: " +
			"a file has an owner, and an agent token inherits its user")
	}
	return p, nil
}
