package main

import (
	"fmt"
	"os"

	"github.com/abcdlsj/sumi/internal/cli"
	"github.com/abcdlsj/sumi/internal/lifecycle"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sumi <server|computer|status|upgrade|version|pair|inbox|read|send|dm|members|follow|unfollow|mute|task|agent|channel>")
		os.Exit(2)
	}
	if code, handled := lifecycle.DispatchServiceCommand(os.Args[1:]); handled {
		os.Exit(code)
	}
	os.Exit(cli.Run(os.Args[1:], os.Stdout))
}
