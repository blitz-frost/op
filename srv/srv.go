// Package srv defines the op server code
package srv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/blitz-frost/op/lib"
)

var (
	stdout *lib.Fmt = lib.Stdout
	stderr *lib.Fmt = lib.Stderr
)

// A prefixer prepends a predetermined byte slice before forwarding writes.
// Buffers until a write ends in a newline.
type prefixer struct {
	dst io.Writer
	buf []byte
	n   int
}

func newPrefixer(prefix []byte, w io.Writer) *prefixer {
	b := make([]byte, len(prefix))
	copy(b, prefix)
	return &prefixer{
		dst: w,
		buf: b,
		n:   len(b),
	}
}

func (x *prefixer) Write(b []byte) (int, error) {
	x.buf = append(x.buf, b...)
	if x.buf[len(x.buf)-1] != '\n' {
		return len(b), nil
	}
	_, err := x.dst.Write(x.buf)
	x.buf = x.buf[:x.n]
	return len(b), err
}

// A config wraps a lib.Proc with pipe targets.
type config struct {
	lib.Proc
	stdout io.Writer
	stderr io.Writer
}

var (
	mainCtx     context.Context
	mainCancel  context.CancelFunc
	ioWg        sync.WaitGroup                       // signal all pipe io terminated
	routesDone  chan struct{}                        // signal all routes terminated
	cleanupDone chan struct{}  = make(chan struct{}) // signal process may safely terminate
	cleanupMux  sync.Mutex                           // guards cleanup channel
)

func init() {
	mainCtx, mainCancel = context.WithCancel(context.Background())

	routesDone = make(chan struct{})
	close(routesDone)
}

func cleanup() {
	cleanupMux.Lock()
	defer cleanupMux.Unlock()

	// noop if already clean
	select {
	case <-cleanupDone:
		return
	default:
	}

	mainCancel()
	// wait for io and routes
	ioWg.Wait()
	<-routesDone

	os.Remove(lib.LockPath)

	close(cleanupDone)
}

func sigint() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)

	<-c
	cleanup()
}

// A procPipe links an io.Writer with an io.Reader.
type procPipe struct {
	dst io.Writer
	src io.Reader
}

// run copies from the Reader to the Writer until an error is encountered.
// NoOp if memebers are nil.
func (x procPipe) run() error {
	// assume either both or none are nil
	if x.dst == nil {
		return nil
	}
	_, err := io.Copy(x.dst, x.src)
	return err
}

// A proc is like a standard library exec.Cmd with context, but uses sigint instead of kill.
// Will fall back to sigkill if process doesn't exit within a timeout.
type proc struct {
	name  string // unique identifier
	route string // parent route

	cancel context.CancelFunc
	done   <-chan struct{}

	cmd *exec.Cmd

	// config values, to use on restart
	inCfg  string
	outCfg string
	errCfg string

	inPipe  procPipe
	outPipe procPipe
	errPipe procPipe
}

func newProc(ctx context.Context, route string, cfg config) (x *proc, err error) {
	var errStr string
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s %s error: %w", cfg.Name, errStr, err)
		}
	}()

	ctx, cancel := context.WithCancel(ctx)

	cmd := exec.Command(cfg.Path, cfg.Args...)
	cmd.Dir = cfg.Dir
	env := make([]string, 0, len(cfg.Env))
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	prefix := []byte(route + "|" + cfg.Name + ": ")

	// setup stdin funnel
	var inPipe procPipe
	if cfg.In != "" {
		inPipe.dst, err = cmd.StdinPipe()
		if err != nil {
			errStr = "stdin"
			return
		}

		inPipe.src, err = os.Open(cfg.In)
		if err != nil {
			errStr = "in file"
			return
		}
	}

	// setup stdout collection
	var outPipe procPipe
	if cfg.Out != "" {
		outPipe.src, err = cmd.StdoutPipe()
		if err != nil {
			errStr = "stdout"
			return
		}

		if cfg.Out == "std" {
			outPipe.dst = newPrefixer(prefix, cfg.stdout)
		} else {
			outPipe.dst, err = os.Create(cfg.Out)
			if err != nil {
				errStr = "out file"
				return
			}
		}
	}

	// setup stderr collection
	var errPipe procPipe
	if cfg.Err != "" {
		errPipe.src, err = cmd.StderrPipe()
		if err != nil {
			errStr = "stderr"
			return
		}

		if cfg.Err == "std" {
			errPipe.dst = newPrefixer(prefix, cfg.stderr)
		} else {
			errPipe.dst, err = os.Create(cfg.Err)
			if err != nil {
				errStr = "err file"
				return
			}
		}
	}

	return &proc{
		name:    cfg.Name,
		route:   route,
		cancel:  cancel,
		done:    ctx.Done(),
		cmd:     cmd,
		inCfg:   cfg.In,
		outCfg:  cfg.Out,
		errCfg:  cfg.Err,
		inPipe:  inPipe,
		outPipe: outPipe,
		errPipe: errPipe,
	}, nil
}

func (x *proc) run() error {
	// start execution
	if err := x.cmd.Start(); err != nil {
		return fmt.Errorf("start error: %w", err)
	}

	// funnel input
	go func() {
		if err := x.inPipe.run(); err != nil && err != io.EOF {
			stderr.Println(x.name+" stdin read error:", err)
		}
		if x.inPipe.dst != nil {
			x.inPipe.dst.(io.Closer).Close()
		}
	}()

	// must read stdout and stderr before cmd.Wait()
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		if err := x.outPipe.run(); err != nil {
			stderr.Println(x.name+" stdout read error:", err)
		}
		wg.Done()
	}()
	go func() {
		if err := x.errPipe.run(); err != nil {
			stderr.Println(x.name+" stderr read error:", err)
		}
		wg.Done()
	}()

	chExit := make(chan error) // inform cancel goroutine process has exited
	chRet := make(chan error)  // final return value from cancel goroutine

	// cancel goroutine
	go func() {
		var err error // return value
		select {
		case err = <-chExit:
			x.cancel() // release context
		case <-x.done:
			if x.inPipe.dst != nil {
				x.inPipe.dst.(io.Closer).Close() // some programs will not exit until stdin is closed
			}
			x.cmd.Process.Signal(os.Interrupt)
			t := time.AfterFunc(10*time.Second, func() {
				x.cmd.Process.Kill()
			})
			<-chExit
			t.Stop()
			err = errors.New("canceled")
		}
		chRet <- err
	}()

	wg.Wait()
	chExit <- x.cmd.Wait()

	return <-chRet
}

var (
	activeMux sync.Mutex
	active    map[string]map[string]*route = make(map[string]map[string]*route) // active routes, mapped by namespace and name
)

func activeGet(namespace, name string) (*route, bool) {
	activeMux.Lock()
	defer activeMux.Unlock()

	ns, ok := active[namespace]
	if !ok {
		return nil, ok
	}

	o, ok := ns[name]
	return o, ok
}

// activeRange applies the given function to all active routes in a specific namespace.
// Concurrent safe.
func activeRange(namespace string, fn func(*route)) {
	activeMux.Lock()
	defer activeMux.Unlock()
	for _, rt := range active[namespace] {
		activeMux.Unlock() // avoid deadlock if fn uses activeMux
		fn(rt)
		activeMux.Lock()
	}
}

// activeRangeAll applies the given function to all active routes.
// Concurrent safe.
func activeRangeAll(fn func(*route)) {
	activeMux.Lock()
	defer activeMux.Unlock()
	for ns, _ := range active {
		activeMux.Unlock() // avoid deadlock
		activeRange(ns, fn)
		activeMux.Lock()
	}
}

func activeRemove(namespace, name string) error {
	activeMux.Lock()
	defer activeMux.Unlock()
	ns, ok := active[namespace]
	if !ok {
		return errors.New("not an active namespace")
	}
	rt, ok := ns[name]
	if !ok {
		return errors.New("not an active route")
	}

	rt.cancel()
	delete(ns, name)
	if len(ns) == 0 {
		delete(active, namespace)
	}

	return nil
}

func activeSet(rt *route) error {
	activeMux.Lock()
	defer activeMux.Unlock()

	ns, ok := active[rt.namespace]
	if !ok {
		ns = make(map[string]*route)
		active[rt.namespace] = ns
	}

	if _, ok := ns[rt.name]; ok {
		return errors.New("already exists")
	}

	ns[rt.name] = rt
	return nil
}

type route struct {
	namespace string
	name      string
	tasks     []config

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // blocks until route has terminated

	mux    sync.Mutex // guard active
	active string     // currently active process name
}

func newRoute(ctx context.Context, namespace, name string, cfgs []lib.Proc, wout, werr io.Writer) *route {
	// wrap raw configs
	// autofill names if absent: process number in route, starting from 0
	tasks := make([]config, len(cfgs))
	for i, _ := range cfgs {
		tasks[i].Proc = cfgs[i]
		if tasks[i].Name == "" {
			tasks[i].Name = strconv.Itoa(i)
		}
		tasks[i].stdout = wout
		tasks[i].stderr = werr
	}

	rtCtx, cfn := context.WithCancel(ctx)

	return &route{
		namespace: namespace,
		name:      name,
		tasks:     tasks,
		ctx:       rtCtx,
		cancel:    cfn,
		done:      make(chan struct{}),
	}
}

func (x *route) activeGet() string {
	x.mux.Lock()
	defer x.mux.Unlock()
	return x.active
}

func (x *route) activeSet(name string) {
	x.mux.Lock()
	x.active = name
	x.mux.Unlock()
}

func (x *route) run() error {
	if err := activeSet(x); err != nil {
		return err
	}

	defer func() {
		activeRemove(x.namespace, x.name)
		close(x.done)
		x.cancel()
	}()
	done := x.ctx.Done()
	for _, cfg := range x.tasks {
		// abort if context canceled
		// needed if cancel triggers exactly between 2 processes
		select {
		case <-done:
			x.activeSet("canceled")
			return errors.New("canceled")
		default:
		}

		p, err := newProc(x.ctx, x.name, cfg)
		if err != nil {
			return fmt.Errorf("%s setup error: %w", p.name, err)
		}
		x.activeSet(p.name)
		if err := p.run(); err != nil {
			x.activeSet(x.active + " error")
			return fmt.Errorf("%s run error: %w", p.name, err)
		}

	}

	x.activeSet("finished")
	return nil
}

// String returns a formated string with the route's name and active process.
func (x *route) String() string {
	r := []byte(x.name)
	r = append(r, '|')
	r = append(r, x.activeGet()...)
	return string(r)
}

// command represents an op program command
type command struct {
	lib.Cmd
	stdout io.Writer // stdout target
	stderr io.Writer // stderr target

	ctx context.Context
}

// executeExit kills all routes and terminates the current program even if it is a dedicated server
func (x command) executeExit() {
	go cleanup()
	<-routesDone
}

// executeKill cancels all active routes.
// If there is an argument, only that route is canceled.
// Waits for termination.
func (x command) executeKill() {
	if x.Route != "" {
		if rt, ok := activeGet(x.Namespace, x.Route); ok {
			rt.cancel()
			<-rt.done
		}
		return
	}

	activeRange(x.Namespace, func(rt *route) {
		rt.cancel()
		<-rt.done
	})
}

// executeList writes a list of active routes to the command's stdout.
// If there is an argument, only the active process of that route is written.
func (x command) executeList() {
	var r []byte
	defer func() {
		x.stdout.Write(r)
	}()

	if x.Route != "" {
		rt, ok := activeGet(x.Namespace, x.Route)
		if !ok {
			return
		}
		r = append([]byte(rt.String()), '\n')
		return
	}

	activeRange(x.Namespace, func(rt *route) {
		r = append(r, rt.String()...)
		r = append(r, '\n')
	})
}

// executeRestart is a shorthand for kill + run.
// Current config may differ from the initial one.
func (x command) executeRestart() error {
	x.executeKill()
	return x.executeRun()
}

// executeRun runs routes as defined by the config found at x.sw.
// x.args may define selective execution within the config:
//
// No arguments -> execute all routes
//
// One argument -> execute specific route
//
// Two arguments -> execute specific process in specific route
func (x command) executeRun() error {
	manifest := x.Config

	// filter as needed
	if x.Route != "" { // narrow to specified route
		rt, ok := manifest[x.Route]
		if !ok {
			return errors.New("route not defined")
		}
		manifest = map[string]lib.Route{x.Route: rt}

		if x.Proc != "" { // narrow to specified process
			i := 0
			for ; i < len(rt.Procs); i++ {
				if rt.Procs[i].Name == x.Proc {
					break
				}
			}
			if i == len(rt.Procs) {
				return errors.New("process not defined")
			}
			rt.Procs = []lib.Proc{rt.Procs[i]}
			manifest[x.Route] = rt
		}
	} else { // if no arguments, filter out non default routes
		for name, rt := range manifest {
			if !rt.Default {
				delete(manifest, name)
			}
		}
	}

	wg := sync.WaitGroup{}
	for name, route := range manifest {
		wg.Add(1)
		go func(namespace, name string, cfgs []lib.Proc) {
			rt := newRoute(x.ctx, namespace, name, cfgs, x.stdout, x.stderr)
			if err := rt.run(); err != nil {
				stderr.Println(name+" error:", err)
			}
			wg.Done()
		}(route.Namespace, name, route.Procs)
	}
	wg.Wait()

	return nil
}

func (x command) run() error {
	switch x.Sw {
	case lib.CmdExit:
		x.executeExit()
	case lib.CmdKill:
		x.executeKill()
	case lib.CmdList:
		x.executeList()
	case lib.CmdRestart:
		return x.executeRestart()
	default:
		return x.executeRun()
	}

	return nil
}

// setup opens 3 pipes in order to communicate with a new client:
//
// [id]_input
//
// [id]_output
//
// [id]_error
func setup(id byte) error {
	paths := lib.PipePaths(id)

	for _, path := range paths {
		if err := syscall.Mkfifo(path, 0600); err != nil {
			os.Remove(paths[0])
			os.Remove(paths[1])
			return err
		}
	}

	ioWg.Add(1)
	return nil
}

// clean removes the pipes of the given client and removes the ID from active IDs
func clean(id byte) {
	paths := lib.PipePaths(id)
	for _, path := range paths {
		os.Remove(path)
	}
	ioWg.Done()
	deleteId(id)
}

// serve uses the appropriate pipes to listen for a new client's requests, and respond to them
func serve(id byte) {
	done := mainCtx.Done()
	pipesOpen := make(chan error, 1)

	var inPipe, outPipe, errPipe *os.File
	defer func() { // in case any pipe are left open
		inPipe.Close()
		outPipe.Close()
		errPipe.Close()
		clean(id)
	}()
	paths := lib.PipePaths(id)
	r := make([]byte, 1) // used to read from input to see when it closes

	// open pipes concurrently to avoid blocking forever in case of abortion
	// OpenFile functions should return when the pipes get closed
	go func() {
		var err error
		inPipe, err = os.OpenFile(paths[0], os.O_RDONLY, os.ModeNamedPipe)
		if err != nil {
			pipesOpen <- fmt.Errorf("input pipe open error: %w", err)
			return
		}
		outPipe, err = os.OpenFile(paths[1], os.O_WRONLY, os.ModeNamedPipe)
		if err != nil {
			pipesOpen <- fmt.Errorf("output pipe open error: %w", err)
			return
		}
		errPipe, err = os.OpenFile(paths[2], os.O_WRONLY, os.ModeNamedPipe)
		if err != nil {
			pipesOpen <- fmt.Errorf("error pipe open error: %w", err)
			return
		}
		pipesOpen <- nil
	}()

	select {
	case <-done:
		return
	case err := <-pipesOpen:
		if err != nil {
			stderr.Println(err)
			return
		}
	}

	dec := json.NewDecoder(inPipe)
	var cmdJson lib.Cmd
	if err := dec.Decode(&cmdJson); err != nil {
		stderr.Println("input parse error:", err)
		return
	}

	ctx, cfn := context.WithCancel(mainCtx)
	cmd := command{
		Cmd:    cmdJson,
		stdout: outPipe,
		stderr: errPipe,
		ctx:    ctx,
	}
	go func() { // keep listening for potential cancel cmd; anything else is ignored
		if err := dec.Decode(&cmdJson); err != nil {
			return
		}
		if cmdJson.Sw == lib.CmdCancel {
			cfn()
		}
	}()

	if err := cmd.run(); err != nil {
		stderr.Println("command run error:", err)
	}

	errPipe.Close()
	outPipe.Close()

	inPipe.Read(r) // wait for other side to close
	inPipe.Close()
}

var (
	idActive map[byte]struct{} = make(map[byte]struct{}) // holds active client ids
	idNext   byte
	idMux    sync.Mutex
)

func deleteId(id byte) {
	idMux.Lock()
	defer idMux.Unlock()
	delete(idActive, id)
}

func getId() byte {
	idMux.Lock()
	defer idMux.Unlock()

	for {
		if _, ok := idActive[idNext]; !ok {
			break
		}
		idNext++
	}

	idActive[idNext] = struct{}{}
	return idNext
}

// register answers client ID http requests
func register(w http.ResponseWriter, r *http.Request) {
	select {
	case <-mainCtx.Done():
		return
	default:
	}

	id := getId()
	if err := setup(id); err != nil {
		stderr.Println("client setup error:", err)
		return
	}
	go serve(id)

	w.Write([]byte{id})
}

func Run() {
	go sigint()
	defer cleanup()

	http.HandleFunc("/", register)
	go func() {
		err := http.ListenAndServe(lib.Port, nil)
		stderr.Println("http server error:", err)
		os.Exit(1)
	}()

	// execute a run command before exiting
	// functions as a server for other op processes until done
	// if server switch is present, runs as dedicated server without executing anything
	// any other switch is invalid
	switch lib.ArgSwitch {
	case lib.CmdServer:
		<-cleanupDone
	case lib.CmdRun:
		conf, err := lib.DecodeConfig()
		if err != nil {
			stderr.Println("manifest decode error:", err)
			return
		}

		cmd := command{
			Cmd: lib.Cmd{
				Sw:        lib.CmdRun,
				Namespace: conf.Namespace,
				Route:     lib.ArgMajor,
				Proc:      lib.ArgMinor,
				Config:    conf.Routes,
			},
			stdout: stdout,
			stderr: stderr,
			ctx:    mainCtx,
		}
		if err := cmd.run(); err != nil {
			stderr.Println("run error:", err)
		}
		ioWg.Wait() // wait for any current clients
	}
}
