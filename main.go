package main

import (
	"fmt"
	"os"

	"github.com/hossein/rahio/cmd"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("client/server mode must be specified")
		os.Exit(1)
	}

	mode := os.Args[1]
	switch mode {
	case "server":
		cmd.StartServer()
	case "client":
		cmd.StartClient()
	default:
		fmt.Printf("Unknown mode: %s\n", mode)
		os.Exit(1)
	}
}
