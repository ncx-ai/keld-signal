package main

import (
	"os"

	"github.com/ncx-ai/keld-signal/internal/agentcli"
)

func main() { os.Exit(agentcli.Execute()) }
