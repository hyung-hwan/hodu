package main

import "fmt"
import "os"
import "strings"

func main() {
	var err error

	if len(os.Args) < 2 {
		goto wrong_usage
	}

	if strings.EqualFold(os.Args[1], "server") {
		if len(os.Args) < 3 {
			goto wrong_usage
		}
		err = server_main(os.Args[2:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: serer error - %s\n", err.Error())
			os.Exit(1)
		}
	} else if strings.EqualFold(os.Args[1], "client") {
		if len(os.Args) < 4 {
			goto wrong_usage
		}
		err = client_main(os.Args[2], os.Args[3:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: client error - %s\n", err.Error())
			os.Exit(1)
		}
	} else {
		goto wrong_usage
	}

	os.Exit(0)

wrong_usage:
	fmt.Fprintf(os.Stderr, "USAGE: %s server listen-addr:listen-port\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "       %s client target-addr:target-port peer-addr:peer-port\n", os.Args[0])
	os.Exit(1)
}
