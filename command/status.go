package main

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/command/flagset"
	"github.com/ethereum/go-ethereum/command/server/proto"
	"github.com/golang/protobuf/ptypes/empty"
)

// StatusCommand is the command to group the peers commands
type StatusCommand struct {
	*Meta
}

// Help implements the cli.Command interface
func (p *StatusCommand) Help() string {
	return `Usage: bor status

  Display the status of the running Bor client.

  ` + p.Flags().Help()
}

func (p *StatusCommand) Flags() *flagset.Flagset {
	flags := p.NewFlagSet("status")

	return flags
}

// Synopsis implements the cli.Command interface
func (c *StatusCommand) Synopsis() string {
	return "Display the status of the client"
}

// Run implements the cli.Command interface
func (c *StatusCommand) Run(args []string) int {
	flags := c.Flags()
	if err := flags.Parse(args); err != nil {
		c.UI.Error(err.Error())
		return 1
	}

	borClt, err := c.BorConn()
	if err != nil {
		c.UI.Error(err.Error())
		return 1
	}

	resp, err := borClt.Status(context.Background(), &empty.Empty{})
	if err != nil {
		c.UI.Error(err.Error())
		return 1
	}

	c.UI.Output(formatStatus(resp))
	return 0
}

func formatStatus(status *proto.StatusResponse) string {
	base := formatKV([]string{
		fmt.Sprintf("Current block hash|%s", status.CurrentBlock.Hash),
		fmt.Sprintf("Current block number|%d", status.CurrentBlock.Number),
		fmt.Sprintf("Current header hash|%s", status.CurrentHeader.Hash),
		fmt.Sprintf("Current header number|%d", status.CurrentHeader.Number),
	})
	return base
}
