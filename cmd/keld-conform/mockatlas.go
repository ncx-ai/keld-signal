package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/ncx-ai/keld-signal/internal/conform/mockatlas"
)

func runMockAtlas(args []string) error {
	fs := flag.NewFlagSet("mockatlas", flag.ContinueOnError)
	port := fs.Int("port", 0, "TCP port on 127.0.0.1 (0 = pick a free one and print it)")
	state := fs.String("state", "", "directory to persist each received body under (required)")
	pending := fs.Int("poll-pending", 0, "how many device polls answer 202 before authorizing")
	token := fs.String("ingest-token", "", "the ingest token to hand out and accept")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *state == "" {
		return errors.New("--state is required")
	}

	srv, err := mockatlas.New(mockatlas.Options{
		StateDir:    *state,
		PollPending: *pending,
		IngestToken: *token,
	})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return err
	}
	// Read by the chain runner to learn the port when --port 0 was used.
	fmt.Printf("mockatlas listening on http://%s\n", ln.Addr().String())
	os.Stdout.Sync()

	if err := http.Serve(ln, srv); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
