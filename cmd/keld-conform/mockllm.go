package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/ncx-ai/keld-signal/internal/conform/mockllm"
)

func runMockLLM(args []string) error {
	fs := flag.NewFlagSet("mockllm", flag.ContinueOnError)
	port := fs.Int("port", 0, "TCP port on 127.0.0.1 (0 = pick a free one and print it)")
	logPath := fs.String("log", "", "append one JSON line per request to this file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	srv, err := mockllm.New(mockllm.Options{LogPath: *logPath})
	if err != nil {
		return err
	}
	defer srv.Close()

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return err
	}
	// The chain runner reads this line to learn the port when --port 0 was
	// used, so it is printed BEFORE Serve blocks and flushed by the newline.
	fmt.Printf("mockllm listening on http://%s\n", ln.Addr().String())
	os.Stdout.Sync()

	if err := http.Serve(ln, srv); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
