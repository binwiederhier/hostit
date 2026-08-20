package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/urfave/cli/v2"

	"heckel.io/hostit/clitable"
)

func execSnapshots(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 1 {
		return errors.New("usage: hostit control app snapshots <name>")
	}
	snaps, err := cl.Snapshots(c.Args().First())
	if err != nil {
		return err
	}
	if len(snaps) == 0 {
		fmt.Println("No snapshots.")
		return nil
	}
	rows := make([][]string, 0, len(snaps))
	for _, s := range snaps {
		kind := "manual"
		if s.Auto {
			kind = "auto"
		}
		rows = append(rows, []string{s.ID, s.CreatedAt.Format("2006-01-02 15:04"), kind, s.Label})
	}
	fmt.Println(clitable.Render([]string{"ID", "CREATED", "KIND", "LABEL"}, rows))
	return nil
}

func execSnapshot(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() < 1 {
		return errors.New("usage: hostit control app snapshot <name> [label]")
	}
	label := strings.Join(c.Args().Slice()[1:], " ")
	snap, err := cl.Snapshot(c.Args().First(), label)
	if err != nil {
		return err
	}
	fmt.Printf("Saved snapshot %s\n", snap.ID)
	return nil
}

func execRollback(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 2 {
		return errors.New("usage: hostit control app rollback <name> <snapshot-id>")
	}
	if err := cl.Rollback(c.Args().First(), c.Args().Get(1)); err != nil {
		return err
	}
	fmt.Printf("%s: rolled back to %s\n", c.Args().First(), c.Args().Get(1))
	return nil
}

func execFork(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() < 2 || c.NArg() > 3 {
		return errors.New("usage: hostit control app fork <source> <new-name> [snapshot-id]")
	}
	a, err := cl.Fork(c.Args().First(), c.Args().Get(1), c.Args().Get(2))
	if err != nil {
		return err
	}
	fmt.Printf("Forked %s into %s (%s)\n", c.Args().First(), a.Name, a.URL)
	return nil
}

func execRemoveSnapshot(c *cli.Context) error {
	cl, err := appsClient(c)
	if err != nil {
		return err
	}
	if c.NArg() != 2 {
		return errors.New("usage: hostit control app rmsnapshot <name> <snapshot-id>")
	}
	if err := cl.DeleteSnapshot(c.Args().First(), c.Args().Get(1)); err != nil {
		return err
	}
	fmt.Printf("%s: deleted snapshot %s\n", c.Args().First(), c.Args().Get(1))
	return nil
}
