package flowy

// Setting a person's password, from the box that already holds the database.
//
// THERE IS NO SIGNUP DOOR, and this verb is why there does not need to be. The
// first account on a node is made by whoever runs the node, at the shell, with
// the DSN in their hand. A login page that can create accounts is a different
// security posture - anybody who can reach the port can make a principal - and
// nobody asked for one.
//
// It reads the password from stdin rather than a flag, because a flag lands in
// the shell history and in the process list, where every other user on the box
// can read it with ps. `flowy passwd --handle dead < secret` or typed at a
// terminal are both fine; a command line is not.

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

func passwdCmd(args []string) error {
	fs := flag.NewFlagSet("passwd", flag.ContinueOnError)
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres-wire DSN (default $DATABASE_URL)")
	node := fs.String("node", envOr("FLOWY_NODE", defaultNode()), "name this node stamps onto every row")
	handle := fs.String("handle", "", "whose password to set")
	// Ending every session is the RIGHT default for a password change: the
	// reason somebody changes one is usually that they think somebody else has
	// it, and leaving the old sessions alive means the change did nothing about
	// the thing they were worried about. It is a flag rather than unconditional
	// so that setting a first password does not read as a security event.
	keep := fs.Bool("keep-sessions", false,
		"leave existing sessions alone (by default a password change signs every browser out)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*handle) == "" {
		return fmt.Errorf("passwd needs --handle, and reads the password from stdin")
	}

	password, err := readPassword(os.Stdin)
	if err != nil {
		return err
	}

	ctx := context.Background()
	db, err := store.Open(ctx, *dsn, *node)
	if err != nil {
		return err
	}
	defer db.Close()

	user, err := db.UserByHandle(ctx, *handle)
	if err != nil {
		return fmt.Errorf("no user with handle %q: %w", *handle, err)
	}
	if err := db.SetPassword(ctx, user.ID, password); err != nil {
		return err
	}
	fmt.Printf("password set for %s (%s)\n", user.Handle, user.ID)

	if !*keep {
		n, err := db.EndSessionsFor(ctx, user.ID)
		if err != nil {
			return err
		}
		// Said out loud, including zero: "0 sessions ended" tells the person
		// changing it that there was nothing live, which is the thing they
		// wanted to know.
		fmt.Printf("%d session(s) ended - pass --keep-sessions to leave them\n", n)
	}
	return nil
}

// readPassword takes everything on stdin, minus one trailing newline.
//
// NOT LINE-BY-LINE. A password may contain anything except a newline, and
// reading "the first line" would silently accept a passphrase whose remainder
// was thrown away - the same shape as bcrypt's 72-byte truncation, which
// SetPassword refuses rather than hides.
func readPassword(in io.Reader) (string, error) {
	buf, err := io.ReadAll(bufio.NewReader(in))
	if err != nil {
		return "", fmt.Errorf("reading the password from stdin: %w", err)
	}
	password := strings.TrimSuffix(string(buf), "\n")
	password = strings.TrimSuffix(password, "\r")
	if password == "" {
		return "", fmt.Errorf("no password on stdin - pipe one in, or type it and press ctrl-d")
	}
	if strings.ContainsAny(password, "\n\r") {
		return "", fmt.Errorf("the password has a newline in it, which no login form can send")
	}
	return password, nil
}
