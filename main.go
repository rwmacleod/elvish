// elvish is an experimental Unix shell. It tries to incorporate a powerful
// programming language with an extensible, friendly user interface.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"os/user"

	"github.com/xiaq/elvish/edit"
	"github.com/xiaq/elvish/eval"
	"github.com/xiaq/elvish/parse"
	"github.com/xiaq/elvish/util"
)

const (
	sigchSize = 32
)

// TODO(xiaq): Currently only the editor deals with signals.
func main() {
	ev := eval.NewEvaluator()
	cmdNum := 0

	username := "???"
	user, err := user.Current()
	if err == nil {
		username = user.Username
	}
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "???"
	}
	rpromptStr := username + "@" + hostname

	sigch := make(chan os.Signal, sigchSize)
	signal.Notify(sigch)

	ed := edit.NewEditor(os.Stdin, ev, sigch)

	for {
		cmdNum++
		name := fmt.Sprintf("<tty %d>", cmdNum)

		prompt := func() string {
			return util.Getwd() + "> "
		}
		rprompt := func() string {
			return rpromptStr
		}

		lr := ed.ReadLine(prompt, rprompt)

		if lr.EOF {
			break
		} else if lr.Err != nil {
			fmt.Println("Editor error:", lr.Err)
			fmt.Println("My pid is", os.Getpid())
		}

		n, pe := parse.Parse(name, lr.Line)
		if pe != nil {
			fmt.Print(pe.(*util.ContextualError).Pprint())
			continue
		}

		ee := ev.Eval(name, lr.Line, n)
		if ee != nil {
			fmt.Print(ee.(*util.ContextualError).Pprint())
			continue
		}
	}
}
