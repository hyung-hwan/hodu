package main

import "flag"
import "fmt"
import "io"
import "os"
import "strings"


func main() {
	var err error
	var flgs *flag.FlagSet

	if len(os.Args) < 2 {
		goto wrong_usage
	}
	if strings.EqualFold(os.Args[1], "server") {
		var la []string

		la = make([]string, 0)

		flgs = flag.NewFlagSet("", flag.ContinueOnError)
		flgs.Func("listen-on", "specify a listening address", func(v string) error {
			la = append(la, v)
			return nil
		})
		flgs.SetOutput(io.Discard) // prevent usage output
		err = flgs.Parse(os.Args[2:])
		if err != nil {
			fmt.Printf ("ERROR: %s\n", err.Error())
			goto wrong_usage
		}

		if len(la) < 0 || flgs.NArg() > 0 {
			goto wrong_usage
		}

		err = server_main(la)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: server error - %s\n", err.Error())
			goto oops
		}
	} else if strings.EqualFold(os.Args[1], "client") {
		var la []string
		var sa []string

		la = make([]string, 0)
		sa = make([]string, 0)

		flgs = flag.NewFlagSet("", flag.ContinueOnError)
		flgs.Func("listen-on", "specify a control channel address", func(v string) error {
			la = append(la, v)
			return nil
		})
		flgs.Func("server", "specify a server address", func(v string) error {
			sa = append(sa, v)
			return nil
		})
		flgs.SetOutput(io.Discard)
		err = flgs.Parse(os.Args[2:])
		if err != nil {
			fmt.Printf ("ERROR: %s\n", err.Error())
			goto wrong_usage
		}

		if len(la) != 1 || len(sa) != 1 || flgs.NArg() < 1 {
			goto wrong_usage
		}
		err = client_main(la[0], sa[0], flgs.Args())
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: client error - %s\n", err.Error())
			goto oops
		}
	} else {
		goto wrong_usage
	}

	os.Exit(0)

wrong_usage:
	fmt.Fprintf(os.Stderr, "USAGE: %s server --listen-on=addr:port\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "       %s client --listen-on=addr:port --server=addr:port peer-addr:peer-port\n", os.Args[0])
	os.Exit(1)

oops:
	os.Exit(1)
}
