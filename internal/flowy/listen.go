package flowy

// `flowy listen` - the listener as ONE process for the life of a session, and
// a WATCHER when the seat's reader is already held.
//
// What it replaces. Every Claude seat on this fleet runs a shell loop around
// `flowy inbox` under a persistent Monitor - flowy-listen-loop-as.sh, forty
// lines of case statements on the exit code, a grep for a fixture room, a
// sleep on refusal. Measured on lab2x1 on 2026-09-14, one seat's transcripts:
// the Monitor was armed 95 times, the listener was inspected by hand 105 times
// (ps, the pid file, the log), and `LISTENER REFUSED` was printed 9 times -
// every time a session re-armed while the previous session's loop was still
// alive. The refusal is correct - two readers under one name split a cursor -
// and it left the new session with no way to hear the room at all, so it
// went looking.
//
// TWO ROLES, ONE VERB, AND THE NAME ON THE ROSTER STAYS TRUE.
//
//	waiter   holds the name, polls the node, prints each delivery as one JSON
//	         line, spools it, acks it. Exactly what `inbox` does, without
//	         exiting: no successor fork, no re-arm, no gap.
//	watcher  the name is held by a LIVE waiter. Rather than refuse, follow that
//	         waiter's spool - the file it writes every page to BEFORE acking -
//	         and print each new line. It hears what the waiter hears, moves no
//	         cursor, appears nowhere on the roster (it IS nothing on the node),
//	         and says on stderr whose deliveries it is following. When that
//	         waiter dies, the watcher takes the name and becomes the waiter.
//
// So a second session on a seat is never deaf and never a second reader.
// `--no-watch` keeps the old behaviour for a caller that wants the refusal.
//
// ONE LINE PER EVENT on stdout, as `inbox` prints it - the shape a Monitor
// turns into a wake-up. Everything else goes to stderr.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const listenUsage = `flowy listen - hear the room for the life of a session, in one process

usage:
  flowy listen --as NAME [--to-me] [--mentions] [--focus P] [--room R]
               [--ignore-room R] [--no-watch]

  --as NAME        the reader label; the same as the seat's name
  --to-me          only what names you, plus a person's unaddressed broadcast
  --mentions       only what names you
  --focus P        everything from project P, only what names you elsewhere
  --room R         only this room
  --ignore-room R  drop this room from what is printed (a test-fixture room)
  --no-watch       refuse when another waiter holds the name, instead of
                   following its deliveries

Run it under a persistent Monitor:

    Monitor(command: flowy listen --as NAME, persistent: true)

It prints one JSON object per delivered message and never exits on its own
unless the token stops working. If NAME's reader is already held by a live
waiter, this becomes a WATCHER of that waiter's spool - same messages, no
second cursor - and takes the name over when the waiter dies.
`

func listenCmd(args []string) error {
	fs := flag.NewFlagSet("listen", flag.ContinueOnError)
	as := fs.String("as", "", "the reader label")
	toMe := fs.Bool("to-me", false, "only what names you, plus a person's unaddressed broadcast")
	mentions := fs.Bool("mentions", false, "only what names you")
	focus := fs.String("focus", "", "everything from this project, only what names you elsewhere")
	room := fs.String("room", "", "only this room")
	ignoreRoom := fs.String("ignore-room", "", "drop this room from what is printed")
	noWatch := fs.Bool("no-watch", false, "refuse rather than watch when the name is held")
	urlFlag := fs.String("url", "", "node to talk to")
	token := fs.String("token", "", "bearer token")
	agent := fs.String("agent", "", agentFlagHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 && fs.Args()[0] == "help" {
		fmt.Print(listenUsage)
		return nil
	}
	if strings.TrimSpace(*as) == "" {
		return errors.New("which seat: pass --as NAME\n\n" + listenUsage)
	}
	base := resolveURL(*urlFlag, os.Getenv("FLOWY_ADDR"))
	bearer, err := resolveToken(*token, os.Getenv("FLOWY_TOKEN"), *agent, os.Getenv("FLOWY_AGENT"))
	if err != nil {
		return err
	}
	if bearer == "" {
		return errNoToken()
	}
	seat := strings.TrimSpace(*agent)
	if seat == "" {
		seat = strings.TrimSpace(os.Getenv("FLOWY_AGENT"))
	}
	if strings.TrimSpace(*token) != "" || strings.TrimSpace(os.Getenv("FLOWY_TOKEN")) != "" {
		seat = ""
	}
	dir, err := waiterDir()
	if err != nil {
		return err
	}
	spool := filepath.Join(dir, "inbox-spool-"+unsafeInName.ReplaceAllString(*as, "-")+".jsonl")
	printLine := lineFilter(*ignoreRoom)
	// THE WATCHER FILTERS FOR ITSELF. The holder's spool has everything the
	// holder asked for, and a watcher that wanted less would otherwise get the
	// holder's level - measured within minutes of the first watcher running:
	// a session armed to hear what named it was handed every agent-to-agent
	// exchange in the room. So the watcher applies wakesFor's own two levels
	// locally, against the principal the token resolves to.
	watchKeep := printLine
	if *toMe || *mentions || *room != "" {
		me, err := whoAmI(base, bearer)
		if err != nil {
			return fmt.Errorf("cannot filter as a watcher without knowing who this token is: %w", err)
		}
		watchKeep = attentionFilter(printLine, me, *toMe, *mentions, *room)
	}

	for {
		lock, err := holdWaiterName(*as)
		if err != nil {
			var held *errWaiterHeld
			if !errors.As(err, &held) || *noWatch {
				return err
			}
			fmt.Fprintf(os.Stderr, "WATCHING: pid %d (%s) holds %q's reader; following what it "+
				"delivers from %s, and taking the name over if it dies\n",
				held.pid, held.kind, *as, spool)
			followSpool(spool, func() bool { return pidAlive(held.pid) }, watchKeep)
			fmt.Fprintf(os.Stderr, "the waiter (pid %d) is gone; taking %q over\n", held.pid, *as)
			continue
		}
		lock.releaseOnSignal()
		fmt.Fprintf(os.Stderr, "LISTENING as %q (pid %d), one JSON line per message\n", *as, os.Getpid())
		err = listenAsWaiter(base, bearer, *as, *room, seat, *toMe, *focus, *mentions, printLine)
		lock.release()
		return err
	}
}

// runningBinary identifies the executable THIS process is running, by the only
// thing that survives being renamed out from under it: the inode it was opened
// on.
//
// /proc/self/exe follows to the running inode even after the path has been
// replaced, which is what makes the comparison below possible at all.
type runningBinary struct {
	path string
	dev  uint64
	ino  uint64
}

// thisBinary reads what is running now. An error means we cannot tell, and the
// caller must treat that as "not stale" rather than as stale - a check that
// cannot read is not a check that found something.
func thisBinary() (runningBinary, error) {
	var b runningBinary
	// The PATH, which may carry a " (deleted)" suffix once the file has been
	// replaced. That suffix is the kernel describing the link, not part of any
	// filename, so it comes off before the path is used for anything.
	link, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return b, err
	}
	b.path = strings.TrimSuffix(link, " (deleted)")

	// The IDENTITY. Stat on /proc/self/exe resolves to the running inode, not
	// to whatever now sits at the path.
	fi, err := os.Stat("/proc/self/exe")
	if err != nil {
		return b, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return b, fmt.Errorf("no stat for /proc/self/exe on this platform")
	}
	b.dev, b.ino = uint64(st.Dev), st.Ino
	return b, nil
}

// replaced reports whether the file at this binary's path is no longer the file
// this process is running.
//
// 01M2FYTJP67A44HJAQQNSRQDH3. `listen` is one process for the life of a session
// and deliberately never re-execs, so a deploy that replaces the binary
// underneath it leaves it running the old one indefinitely - delivering
// messages perfectly well while missing whatever the new build added. The
// symptom is never "my listener broke", it is "the feature you shipped is not
// here", reported days later by somebody with no reason to suspect the age of
// their own process.
//
// An `inbox` loop does not have this problem: it exits at every deadline and
// the next iteration opens whatever is at the path now. Measured on .78 - a
// reader on a replaced inode at 12:20 was on the new one by 12:35, with nobody
// restarting anything. The better the listener, the staler it gets.
//
// WHY (dev,inode) AND NOT A VERSION STRING OR A HASH. A version names a COMMIT:
// two clients built from 2c729ff on this fleet differ by 25MB and stamp the
// same string, so comparing stamps agrees with everything. The NODE's hash
// answers nothing either - the node is a different binary from the client, and
// its identity says nothing about ours. What is actually being asked is "is the
// file I was started from still the file I am running", and that is a question
// about one machine, needing no node round trip and no agreement about naming.
//
// A missing or unreadable path is NOT stale: the check failing is different
// from the check finding something, and reporting the first as the second is
// how a monitor cries wolf at a mount that blipped.
func (b runningBinary) replaced() bool {
	if b.path == "" {
		return false
	}
	fi, err := os.Stat(b.path)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return uint64(st.Dev) != b.dev || st.Ino != b.ino
}

// listenAsWaiter is `inbox`'s loop, run until something is wrong with the
// credential. A delivery and a quiet deadline both just go round again.
func listenAsWaiter(base, bearer, as, room, seat string, toMe bool, focus string, mentions bool,
	printLine func(string) bool,
) error {
	client := &http.Client{Timeout: (inboxPollWindow + 30) * time.Second}

	// THE STALENESS REPORT GOES AROUND THE ATTENTION FILTER, deliberately.
	// Taken before filterStdout swaps os.Stdout below: the filters exist to
	// decide which MESSAGES concern this seat, and a line saying this process
	// is running a replaced binary is not a message and concerns it whatever
	// --to-me or --mentions say. Passing it through attentionFilter would let
	// "not addressed to you" silence the one line that is always about you.
	stream := os.Stdout
	self, selfErr := thisBinary()

	// The room filter for a printed line rides on stdout: waitOnInbox prints
	// through writeInbox, so an ignored room is filtered by re-reading what it
	// wrote. Simpler: hand waitOnInbox a stdout that filters.
	restore := filterStdout(printLine)
	defer restore()
	backoff := firstInboxBackoff
	said := false
	for {
		// Once per poll, and only ever ONE line: a listener that repeated this
		// every deadline would be the flooding watcher this fleet already
		// stopped twice.
		if !said && selfErr == nil && self.replaced() {
			said = true
			if out, err := json.Marshal(map[string]any{
				"type": "stale_binary",
				"path": self.path,
				"note": "the file this listener was started from has been replaced; " +
					"it is still running the old one and will until the session restarts",
			}); err == nil {
				fmt.Fprintln(stream, string(out))
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(defaultInboxDeadline)*time.Second+time.Minute)
		err := waitOnInbox(ctx, client, base, bearer, as, room, bearer, seat, toMe, focus, mentions, defaultInboxDeadline)
		cancel()
		switch {
		case err == nil, errors.Is(err, errQuietDeadline):
			backoff = firstInboxBackoff
			continue
		case strings.Contains(err.Error(), "changed while this waiter was running"),
			strings.Contains(err.Error(), "no inbox reader called"):
			// The credential, not the network: nothing here can fix it, and
			// polling on would be hearing nothing while looking attached.
			return err
		default:
			fmt.Fprintf(os.Stderr, "listen: %v; retrying in %s\n", err, backoff)
			time.Sleep(backoff)
			if backoff < maxInboxBackoff {
				backoff *= 2
			}
		}
	}
}

// lineFilter decides whether a delivered line is printed: everything, minus
// one ignored room. The room is read from the JSON so a body that happens to
// mention the room's name is not dropped with it.
func lineFilter(ignoreRoom string) func(string) bool {
	if strings.TrimSpace(ignoreRoom) == "" {
		return func(string) bool { return true }
	}
	return func(line string) bool {
		var e struct {
			Room string `json:"room"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return true
		}
		return e.Room != ignoreRoom
	}
}

// attentionFilter is wakesFor's `addressed` and `mentionsOnly`, applied to a
// spooled line by a watcher: named or nothing under --mentions; named, or a
// person's unaddressed broadcast, under --to-me; one room under --room. The
// definitions are the node's (inbox.go); only where they run is different.
func attentionFilter(inner func(string) bool, me principalIDs, toMe, mentions bool, room string) func(string) bool {
	return func(line string) bool {
		if !inner(line) {
			return false
		}
		var e struct {
			Room      string `json:"room"`
			Addressee string `json:"addressee"`
			Private   bool   `json:"private"`
			Type      string `json:"type"`
			Meta      struct {
				ActorKind string `json:"actor_kind"`
			} `json:"meta"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return true
		}
		if room != "" && e.Room != room {
			return false
		}
		named := e.Addressee != "" && (e.Addressee == me.user || e.Addressee == me.agent)
		// A note reached the spool because the node decided it was for this
		// seat; a DM names you by construction.
		if e.Type == "todo.note" || e.Private {
			return true
		}
		if mentions {
			return named
		}
		if toMe {
			return named || (e.Meta.ActorKind == "user" && e.Addressee == "")
		}
		return true
	}
}

type principalIDs struct {
	user, agent string
}

// whoAmI resolves the token to its ids, for the addressee test.
func whoAmI(base, bearer string) (principalIDs, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var w struct {
		User  string `json:"user"`
		Agent string `json:"agent"`
	}
	if err := peerRequest(ctx, &http.Client{Timeout: 30 * time.Second}, http.MethodGet,
		base+"/api/whoami", bearer, nil, &w); err != nil {
		return principalIDs{}, err
	}
	return principalIDs{user: w.User, agent: w.Agent}, nil
}

// filterStdout swaps os.Stdout for a pipe whose lines pass through keep. The
// waiter prints through writeInbox, which writes os.Stdout; this is how one
// ignored room is dropped without teaching writeInbox about filters. Returns
// the function that puts stdout back and drains the pipe.
func filterStdout(keep func(string) bool) func() {
	real := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return func() {}
	}
	os.Stdout = w
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if keep(line) {
				fmt.Fprintln(real, line)
			}
		}
	}()
	return func() {
		w.Close()
		<-done
		os.Stdout = real
	}
}

// followSpool prints every line appended to path from now on, until alive
// reports false. It starts at the END: what the waiter delivered before this
// watcher existed was somebody else's wake-up, and `inbox replay` reads it.
//
// Polled, not inotify: the file is appended by another process a few times an
// hour, and a dependency for that would be the wrong trade. Half a second is
// well under the time a session takes to act on a line.
func followSpool(path string, alive func() bool, keep func(string) bool) {
	var offset int64
	if st, err := os.Stat(path); err == nil {
		offset = st.Size()
	}
	var partial string
	for {
		f, err := os.Open(path)
		if err == nil {
			if st, err := f.Stat(); err == nil && st.Size() < offset {
				// Truncated or rotated: start over from the top of the new file.
				offset = 0
			}
			if _, err := f.Seek(offset, io.SeekStart); err == nil {
				chunk, _ := io.ReadAll(f)
				offset += int64(len(chunk))
				text := partial + string(chunk)
				lines := strings.Split(text, "\n")
				partial = lines[len(lines)-1]
				for _, line := range lines[:len(lines)-1] {
					if strings.TrimSpace(line) == "" {
						continue
					}
					if keep(line) {
						fmt.Println(line)
					}
				}
			}
			f.Close()
		}
		if !alive() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// pidAlive is `kill -0`: exists and is ours, or exists and is somebody else's.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
