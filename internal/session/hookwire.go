package session

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Environment variables a Session's hooks read. The adapter sets them in the
// pane, next to the spool's (ADR 0018); a hook runs as the CLI's own process
// and inherits them.
const (
	EnvHookSocket = "ARCHWRIGHT_HOOK_SOCKET" // the Works' hook receiver
	EnvHookFile   = "ARCHWRIGHT_HOOK_FILE"   // the run's hook spool file, the fallback
	EnvHookToken  = "ARCHWRIGHT_HOOK_TOKEN"
	envRun        = "ARCHWRIGHT_RUN"
)

// maxSocketPath is the longest unix socket path macOS binds: sun_path holds 104 bytes with its NUL.
const maxSocketPath = 103

// maxLine bounds one delivery; a hook payload is a few KB, a tool input can be larger.
const maxLine = 1 << 20

// Delivery is one hook as it travels to the Works: the payload with who sent
// it. ID is unique per hook invocation, so a hook that reached the Works both
// over the socket and through the spool file counts once.
type Delivery struct {
	ID      string          `json:"id"`
	Run     string          `json:"run"`
	Token   string          `json:"token"`
	Payload json.RawMessage `json:"payload"`
}

// SendHook is the whole of a Session's hook (`archwright session-hook`): it
// appends the payload read from in to the run's spool file and then posts it to
// the Works' receiver. The spool file is written first and is the fallback; a
// failed post is not an error. The hook is observe-only: it returns nothing
// the CLI could read as a decision, so it never prints to out and the caller
// exits 0 whatever happens here.
func SendHook(getenv func(string) string, in io.Reader) error {
	payload, err := io.ReadAll(io.LimitReader(in, maxLine))
	if err != nil {
		return err
	}
	d := Delivery{ID: newID(), Run: getenv(envRun), Token: getenv(EnvHookToken), Payload: json.RawMessage(payload)}
	if !json.Valid(payload) {
		return errors.New("the hook payload is not JSON")
	}
	line, err := json.Marshal(d)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	var werr error
	if f := getenv(EnvHookFile); f != "" {
		werr = appendFile(f, line)
	}
	if sock := getenv(EnvHookSocket); sock != "" {
		if err := post(sock, line); err == nil {
			return nil // the Works has it; the spool copy is deduplicated by id
		}
	}
	return werr
}

func appendFile(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line) // one write: concurrent hooks never interleave a line
	return err
}

// post sends one line and waits for the receiver's ack.
func post(sock string, line []byte) error {
	c, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write(line); err != nil {
		return err
	}
	ack, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return err
	}
	if strings.TrimSpace(ack) != "ok" {
		return fmt.Errorf("the Works refused the hook: %s", strings.TrimSpace(ack))
	}
	return nil
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Receiver is the Works' end of the hooks: a unix socket on a short path that
// takes one line per connection and hands it to the Session registered for
// its run. It listens only for the user (mode 0700 directory) and checks each
// delivery's token, since the hooks it serves are observe-only but state
// follows them.
type Receiver struct {
	ln   net.Listener
	path string
	dir  string
	wg   sync.WaitGroup

	mu   sync.Mutex
	subs map[string]*sub
}

type sub struct {
	token string
	fn    func(Delivery)
}

// NewReceiver listens on a socket inside dir, which must keep the whole path
// under the platform's limit; it is created private to the user.
func NewReceiver(dir string) (*Receiver, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "h-"+hex.EncodeToString(b)+".sock")
	if len(path) > maxSocketPath {
		return nil, fmt.Errorf("the hook socket path %q is %d bytes; a unix socket path may be at most %d", path, len(path), maxSocketPath)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	r := &Receiver{ln: ln, path: path, dir: dir, subs: map[string]*sub{}}
	r.wg.Add(1)
	go r.serve()
	return r, nil
}

// Path is the socket's path, for EnvHookSocket.
func (r *Receiver) Path() string { return r.path }

// Register sends the hooks of run, sent with token, to fn until the returned
// func is called. fn runs on the connection's goroutine and must not block.
func (r *Receiver) Register(run, token string, fn func(Delivery)) (unregister func()) {
	r.mu.Lock()
	r.subs[run] = &sub{token: token, fn: fn}
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.subs, run)
		r.mu.Unlock()
	}
}

// Close stops listening and waits for the connections in flight.
func (r *Receiver) Close() {
	r.ln.Close()
	r.wg.Wait()
	os.Remove(r.path)
}

func (r *Receiver) serve() {
	defer r.wg.Done()
	for {
		c, err := r.ln.Accept()
		if err != nil {
			return
		}
		r.wg.Go(func() {
			defer c.Close()
			r.handle(c)
		})
	}
}

func (r *Receiver) handle(c net.Conn) {
	c.SetDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReaderSize(c, 64<<10).ReadBytes('\n')
	if err != nil || len(line) > maxLine {
		io.WriteString(c, "bad\n")
		return
	}
	var d Delivery
	if json.Unmarshal(line, &d) != nil {
		io.WriteString(c, "bad\n")
		return
	}
	if !r.deliver(d) {
		io.WriteString(c, "unknown run\n")
		return
	}
	io.WriteString(c, "ok\n")
}

// deliver hands d to its run's Session, if its token is right.
func (r *Receiver) deliver(d Delivery) bool {
	r.mu.Lock()
	s := r.subs[d.Run]
	r.mu.Unlock()
	if s == nil || subtle.ConstantTimeCompare([]byte(d.Token), []byte(s.token)) != 1 {
		return false
	}
	s.fn(d)
	return true
}
