// Package cli defines the op client code.
package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync"

	"github.com/blitz-frost/op/lib"
)

var (
	stdout *lib.Fmt = lib.Stdout
	stderr *lib.Fmt = lib.Stderr
)

var inPipe *os.File

// On interrupt, announce server to cancel the current request.
// Main routine will terminate when server closes output and error pipes.
func sigint() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
	cmd := lib.Cmd{Sw: lib.CmdCancel}
	sendCmd(cmd)
}

// sendCmd encodes and sends the given command. Must not be called before opening the input pipe.
func sendCmd(cmd lib.Cmd) error {
	enc := json.NewEncoder(inPipe)
	return enc.Encode(cmd)
}

func Run() {
	go sigint()

	conf, err := lib.DecodeConfig()
	if err != nil {
		stderr.Println("manifest decode error:", err)
		return
	}

	resp, err := http.Get("http://localhost" + lib.Port + "/")
	if err != nil {
		stderr.Println("http error:", err)
		return
	}
	defer resp.Body.Close()

	if resp.ContentLength == 0 {
		stderr.Println("refused by server")
		return
	}

	r := make([]byte, 1)
	resp.Body.Read(r)

	var outPipe, errPipe *os.File
	defer func() {
		inPipe.Close()
		outPipe.Close()
		errPipe.Close()
	}()
	paths := lib.PipePaths(r[0])

	inPipe, err = os.OpenFile(paths[0], os.O_WRONLY, os.ModeNamedPipe)
	if err != nil {
		stderr.Println("input pipe open error: %w", err)
		return
	}
	outPipe, err = os.OpenFile(paths[1], os.O_RDONLY, os.ModeNamedPipe)
	if err != nil {
		stderr.Println("output pipe open error: %w", err)
		return
	}
	errPipe, err = os.OpenFile(paths[2], os.O_RDONLY, os.ModeNamedPipe)
	if err != nil {
		stderr.Println("error pipe open error: %w", err)
		return
	}

	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		if _, err := io.Copy(stdout, outPipe); err != nil {
			stderr.Println("stdout error:", err)
		}
		wg.Done()
	}()
	go func() {
		if _, err := io.Copy(stderr, errPipe); err != nil {
			stderr.Println("stderr error:", err)
		}
		wg.Done()
	}()

	// encode and send command
	cmd := lib.Cmd{
		Sw:        lib.ArgSwitch,
		Namespace: conf.Namespace,
		Route:     lib.ArgMajor,
		Proc:      lib.ArgMinor,
		Config:    conf.Routes,
	}
	if err := sendCmd(cmd); err != nil {
		stderr.Println("command send error:", err)
		return
	}

	wg.Wait()
}
