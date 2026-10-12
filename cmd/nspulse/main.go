package main

import (
	"fmt"
	"os"

	"github.com/shaun/nspulse/internal/cli"
	"github.com/shaun/nspulse/internal/daemon"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "daemon":
		err = daemon.Run(os.Args[2:])
	case "status":
		err = cli.Status(os.Args[2:])
	case "watch":
		err = cli.Watch(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "nspulse %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `nspulse - monitor DNS response times and keep the fastest server first

Usage:
  nspulse daemon [flags]   run the monitoring/switching daemon (does the switching)
  nspulse status [flags]   print current hour measurements + probable candidate (read-only)
  nspulse watch [flags]    live view of the current hour (read-only)

Run 'nspulse <command> -h' for command flags.
`)
}
