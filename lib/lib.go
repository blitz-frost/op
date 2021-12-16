// Package lib defines common code between op server and clients
package lib

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"text/template"

	"gopkg.in/yaml.v2"
)

var (
	BasePath     string           // general pipe directory
	LockPath     string           // server lock file
	ConfigPath   string           // config file path
	TemplatePath string           // template file path
	MetaPath     string           // meta file path
	Port         string = ":2048" // server port
)

var (
	ArgSwitch CmdSwitch // execution switch
	ArgMajor  string    // route to execute, or meta variant to apply
	ArgMinor  string    // proc to execute
)

func init() {
	uid := os.Getuid()
	BasePath = "/run/user/" + strconv.Itoa(uid) + "/op"
	if err := os.Mkdir(BasePath, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		fmt.Println("base path make error:", err)
		os.Exit(1)
	}
	LockPath = BasePath + "/lock"

	parseArgs()

	TemplatePath = os.Getenv("OP_TEMPLATE")
	if TemplatePath == "" {
		TemplatePath = "op_template.yaml"
	}

	MetaPath = os.Getenv("OP_META")
	if MetaPath == "" {
		MetaPath = "op_meta.yaml"
	}
}

// parseArgs interprets the command line arguments.
func parseArgs() {
	m := make(map[CmdSwitch]struct{})

	// read switches until the first undefined argument
	var i int
	for i = 1; i < len(os.Args); i++ {
		if !isNotRun(os.Args[i]) {
			break
		}
		sw := CmdSwitch(os.Args[i])

		// repeating switches are invalid
		if _, ok := m[sw]; ok {
			fmt.Println("invalid command line")
			os.Exit(1)
		}

		m[CmdSwitch(os.Args[i])] = struct{}{}
	}

	// if global switch is present, use global manifest
	if _, ok := m[CmdGlobal]; ok {
		ConfigPath = os.Getenv("OPGLOBAL")
		delete(m, CmdGlobal)
	} else {
		ConfigPath = os.Getenv("OP")
		if ConfigPath == "" {
			ConfigPath = "op.yaml"
		}
	}

	// currently, only up to one switch may be provided, apart from CmdGlobal
	if len(m) > 1 {
		fmt.Println("invalid command line")
		os.Exit(1)
	}

	for sw, _ := range m {
		ArgSwitch = sw
	}

	// first undefined argument is interpreted as the target route
	if i >= len(os.Args) {
		return
	}
	ArgMajor = os.Args[i]

	// second undefined argument is interpreted as the target proc
	if i++; i >= len(os.Args) {
		return
	}
	ArgMinor = os.Args[i]
}

// PipePaths returns the full paths for the pipe set to be used by the client with given id.
func PipePaths(id byte) [3]string {
	idS := strconv.FormatUint(uint64(id), 10)
	return [3]string{
		BasePath + "/" + idS + "_input",
		BasePath + "/" + idS + "_output",
		BasePath + "/" + idS + "_error",
	}
}

// A Fmt wraps an io.Writer to be concurrent safe.
// Also provides fmt package formating.
type Fmt struct {
	dst io.Writer
	mux sync.Mutex
}

func NewFmt(w io.Writer) *Fmt {
	return &Fmt{dst: w}
}

func (x *Fmt) Write(b []byte) (int, error) {
	x.mux.Lock()
	defer x.mux.Unlock()
	return x.dst.Write(b)
}

func (x *Fmt) Print(a ...interface{}) (n int, err error) {
	x.mux.Lock()
	defer x.mux.Unlock()
	return x.dst.Write([]byte(fmt.Sprint(a...)))
}

func (x *Fmt) Println(a ...interface{}) (n int, err error) {
	x.mux.Lock()
	defer x.mux.Unlock()
	return x.dst.Write([]byte(fmt.Sprintln(a...)))
}

var (
	Stdout *Fmt = NewFmt(os.Stdout)
	Stderr *Fmt = NewFmt(os.Stderr)
)

type CmdSwitch string

const (
	CmdCancel  CmdSwitch = "-c" // cancel client command; not for end users
	CmdExit              = "-e" // shut down dedicated server
	CmdGlobal            = "-g" // global switch; only valid as a command line arg
	CmdKill              = "-k" // kill routes
	CmdList              = "-l" // list active routes
	CmdMeta              = "-m" // generate config from template and meta
	CmdPrint             = "-p" // print config routes
	CmdRestart           = "-r" // restart routes
	CmdRun               = ""   // run routes
	CmdServer            = "-s" // run as dedicated server
)

var switchMap = map[CmdSwitch]struct{}{
	CmdCancel:  struct{}{},
	CmdExit:    struct{}{},
	CmdGlobal:  struct{}{},
	CmdKill:    struct{}{},
	CmdList:    struct{}{},
	CmdMeta:    struct{}{},
	CmdPrint:   struct{}{},
	CmdRestart: struct{}{},
	CmdServer:  struct{}{},
}

// isNotRun returns true if the argument is one of the defined command switches.
func isNotRun(s string) bool {
	sw := CmdSwitch(s)
	_, ok := switchMap[sw]
	return ok
}

// A Proc holds the information necessary to execute a process.
type Proc struct {
	Name string
	Path string
	Dir  string
	Env  map[string]string
	Args []string
	Out  string
	Err  string
}

// A Route holds information relevant to a single execution route.
type Route struct {
	Default bool              // will run on no-argument forms
	Env     map[string]string // route-wide env
	Procs   []Proc            // process configurations
}

// A Manifest holds routes and their individual process configs.
type Manifest struct {
	Env    map[string]string
	Routes map[string]Route
}

func MakeManifest() Manifest {
	return Manifest{
		Env:    make(map[string]string),
		Routes: make(map[string]Route),
	}
}

// Cmd represents an op program command
type Cmd struct {
	Sw     CmdSwitch        // command switch
	Route  string           // target route
	Proc   string           // target proc
	Config map[string]Route // manifest to use for command; may be nil for commands that don't need it
}

type Meta struct {
	Active   string
	Variants map[string]map[string]string
}

// DecodeMeta returns a meta object from "op_meta.yaml" in the current directory.
// If the OP_META env is set, decodes from there instead.
func DecodeMeta() (Meta, error) {
	x := Meta{}

	b, err := os.ReadFile(MetaPath)
	if err != nil {
		return x, err
	}

	if err := yaml.Unmarshal(b, &x); err != nil {
		return x, err
	}

	return x, nil
}

// UpdateMeta updates the meta file to the specified meta.
func UpdateMeta(m Meta) error {
	f, err := os.Create(MetaPath)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := yaml.NewEncoder(f)

	return enc.Encode(m)
}

// ExecuteTemplate reads the template from "op_template.yaml" in the current directory and applies the specified variant to it from working meta.
// If the OP_TEMPLATE env is set, reads the template from there instead.
// See DecodeMeta for meta specifications.
func ExecuteTemplate(variant string) error {
	m, err := DecodeMeta()
	if err != nil {
		return err
	}

	// check if desired variant actually exists
	vr, ok := m.Variants[variant]
	if !ok {
		return errors.New("variant not defined")
	}

	b, err := os.ReadFile(TemplatePath)
	if err != nil {
		return err
	}

	tmpl := template.New("test")
	if _, err := tmpl.Parse(string(b)); err != nil {
		return err
	}

	f, err := os.Create(ConfigPath)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := tmpl.Execute(f, vr); err != nil {
		return err
	}

	m.Active = variant
	UpdateMeta(m)

	return nil
}

// expandEnv replaces env markers in the input text with their corresponding env values.
//
// env marker: ${NAME}
func expandEnv(b []byte) []byte {
	r := make([]byte, 0, len(b))

	var last byte
	var i int // bytes copied from b
	for j := 0; j < len(b); j++ {
		if b[j] == '$' {
			// copy from what is missing
			r = append(r, b[i:j]...)

			// just replace the previous \ with $ if it was escaped, otherwise variable expansion
			if last == '\\' {
				r[len(r)-1] = '$'
			} else {
				iVar := j + 1    // open brace position in b
				jVar := iVar + 1 // close brace position in b

				// don't overflow; skip unexpected syntax
				if iVar < len(b) && b[iVar] == '{' {
					// find closing brace
					for jVar < len(b) && b[jVar] != '}' {
						jVar++
					}

					// do nothing if not found
					if jVar < len(b) {
						envName := string(b[iVar+1 : jVar]) // don't include the braces
						env := os.Getenv(envName)

						// add env value to b
						r = append(r, env...)

						// skip corresponding part of b0
						j = jVar
					}
				}
			}

			// update copy index
			i = j + 1
		}
		last = b[j]
	}
	r = append(r, b[i:len(b)]...)

	return r
}

// DecodeConfig returns the manifest found at config path ("op.yaml" by default).
func DecodeConfig() (map[string]Route, error) {
	// read manifest
	b0, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("config open error: %w", err)
	}

	// variable expansion
	b := expandEnv(b0)

	// decode manifest
	x := Manifest{}
	if err := yaml.Unmarshal(b, &x); err != nil {
		return nil, fmt.Errorf("config parse error: %w", err)
	}

	// roll out env declarations from top to bottom
	// bottom has priority
	routeMap := x.Routes
	for _, route := range routeMap {
		// merge route env with global env
		routeEnv := route.Env
		if routeEnv == nil {
			if x.Env == nil {
				continue // if both route and global env are empty, nothing needs to be done
			}
			routeEnv = x.Env
		} else {
			for k, v := range x.Env {
				if _, ok := routeEnv[k]; !ok {
					routeEnv[k] = v
				}
			}
		}

		procMap := route.Procs
		for procKey, proc := range procMap {
			// merge proc env with route env
			envMap := proc.Env
			if envMap == nil {
				envMap = make(map[string]string)
				procMap[procKey].Env = envMap
			}
			for k, v := range routeEnv {
				if _, ok := envMap[k]; !ok {
					envMap[k] = v
				}
			}
		}
	}

	return x.Routes, nil
}
