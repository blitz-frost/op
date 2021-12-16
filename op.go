package op

import (
	"errors"
	"fmt"
	"os"

	"github.com/blitz-frost/op/cli"
	"github.com/blitz-frost/op/lib"
	"github.com/blitz-frost/op/srv"
)

func Run() {
	// on print switch, print routes found in config file and exit
	//
	// on meta switch with no further arguments - print variants and active variant
	// otherwise applies the next arg as template variant
	switch lib.ArgSwitch {
	case lib.CmdPrint:
		manifest, err := lib.DecodeConfig()
		if err != nil {
			fmt.Println(err)
			return
		}
		for name, rt := range manifest {
			s := ""
			if rt.Default {
				s = " - default"
			}
			fmt.Println(name + s)
		}
		return

	case lib.CmdMeta:
		if lib.ArgMajor == "" {
			meta, err := lib.DecodeMeta()
			if err != nil {
				fmt.Println(err)
				return
			}

			fmt.Println("Defined variants:")
			for k, _ := range meta.Variants {
				fmt.Println(k)
			}
			fmt.Println("Active: " + meta.Active)
		} else {
			if err := lib.ExecuteTemplate(lib.ArgMajor); err != nil {
				fmt.Println(err)
				return
			}
		}
		return
	}

	// if lock file already exists, run as client
	// otherwise run as server
	asSrv := true
	if _, err := os.OpenFile(lib.BasePath+"/lock", os.O_CREATE|os.O_EXCL, 0000); err != nil {
		if !errors.Is(err, os.ErrExist) {
			fmt.Println("lock file creation error:", err)
			return
		}
		asSrv = false
	}

	if asSrv {
		srv.Run()
	} else {
		cli.Run()
	}
}
