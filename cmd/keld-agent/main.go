package main

import (
	"os"

	"github.com/ncx-ai/keld-cli/internal/agentcli"
)

func main() { os.Exit(agentcli.Execute()) }
